package capture

/*
#include <stdlib.h>
#include "bridge.h"
*/
import "C"

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"runtime/pprof"
	"slices"
	"strings"
	"time"
	"unsafe"

	"github.com/waarispierre/recordo/internal/clock"
	"github.com/waarispierre/recordo/internal/config"
	"github.com/waarispierre/recordo/internal/frames"
	"github.com/waarispierre/recordo/internal/pauses"
	"github.com/waarispierre/recordo/internal/session"
	"github.com/waarispierre/recordo/internal/telemetry"
)

// TargetKind is what to point the camera at.
type TargetKind int

const (
	// TargetAsk lists what is on screen and asks.
	TargetAsk TargetKind = iota
	TargetDisplay
	TargetApp
	TargetWindow
)

// Target names what to record.
type Target struct {
	Kind     TargetKind
	App      string // TargetApp: case-insensitive substring of the app name
	WindowID uint32 // TargetWindow
}

// Chooser asks which window to record. ok is false for the whole display.
type Chooser func(windows []Window) (w Window, ok bool, err error)

// Plan is what will be recorded, resolved before anything starts.
type Plan struct {
	Window   *Window // nil records the whole display
	Display  Display
	Label    string
	CaptureW uint32
	CaptureH uint32
	Scale    uint32
	// Microphone is the name of the microphone being recorded, or "" when voice over is
	// off.
	Microphone string
	// Webcam is the name of the camera being recorded, or "" when the overlay is off or
	// the camera failed to open.
	Webcam string
	// Camera is the open webcam while recording, for a caller that wants its live
	// preview (see WebcamPreview). nil when there is no webcam.
	Camera *Webcam
}

// MakePlan resolves what will be recorded, without starting anything.
func MakePlan(target Target, choose Chooser) (*Plan, error) {
	content, err := ShareableContent()
	if err != nil {
		return nil, err
	}
	if len(content.Displays) == 0 {
		return nil, errors.New("no displays found")
	}
	display := content.Displays[0]

	var window *Window
	switch target.Kind {
	case TargetAsk:
		w, ok, err := choose(content.Recordable())
		if err != nil {
			return nil, err
		}
		if ok {
			window = &w
		}
	case TargetApp:
		w, ok := LargestForApp(content.Recordable(), target.App)
		if !ok {
			// Distinguish "no such app" from "its windows are all out of view".
			if hidden, any := LargestForApp(Capturable(content.Windows), target.App); any {
				return nil, fmt.Errorf("%s has no window on screen — its windows are on another Space, "+
					"hidden by Stage Manager, or minimised. Bring one onto the screen, or record the whole display",
					hidden.AppName)
			}
			return nil, fmt.Errorf("no window found for %q — try `recordo windows`", target.App)
		}
		window = &w
	case TargetWindow:
		all := Capturable(content.Windows)
		i := slices.IndexFunc(all, func(w Window) bool { return w.ID == target.WindowID })
		if i < 0 {
			return nil, fmt.Errorf("no window with id %d — try `recordo windows`", target.WindowID)
		}
		window = &all[i]
	}

	var windowID uint32
	label := ""
	if window != nil {
		// Record on the display the window is on, not just the first one: with two
		// screens, the window is often on the other.
		d, ok := displayFor(content.Displays, window.Frame)
		if !ok {
			// The capture is cropped from one display, so a window running past its edge
			// would come out cut off, with black where the rest should be.
			f := window.Frame
			if _, visible := displayAt(content.Displays, f.X+f.W/2, f.Y+f.H/2); visible {
				return nil, fmt.Errorf("%s runs past the edge of the screen (%s). "+
					"Move it fully onto one screen, or record the whole display", window.Label(), f)
			}
			return nil, fmt.Errorf("%s is not on screen (%s), so it is on another Space or hidden. "+
				"Switch to it first, or record the whole display", window.Label(), f)
		}
		display, label, windowID = d, window.Label(), window.ID
	} else {
		label = fmt.Sprintf("entire display (%dx%d)", display.Width, display.Height)
	}
	rect := display.Frame
	if window != nil {
		rect = window.Frame
	}

	s, err := PointPixelScale(display.ID, windowID)
	if err != nil {
		return nil, err
	}
	// Points, not pixels: capturing a Retina panel at 1x throws away half the detail,
	// which shows as soft text the moment the camera zooms in.
	scale := uint32(max(math.Round(s), 1))
	return &Plan{
		Window:   window,
		Display:  display,
		Label:    label,
		CaptureW: uint32(rect.W) * scale,
		CaptureH: uint32(rect.H) * scale,
		Scale:    scale,
	}, nil
}

// displayFor is the display that wholly contains a window frame. Windows parked on
// another Space report a frame outside every display, which would silently crop to an
// empty region and record pure black, so they match none.
func displayFor(displays []Display, f Rect) (Display, bool) {
	for _, d := range displays {
		if f.X >= d.Frame.X && f.Y >= d.Frame.Y &&
			f.X+f.W <= d.Frame.X+d.Frame.W+1 && f.Y+f.H <= d.Frame.Y+d.Frame.H+1 {
			return d, true
		}
	}
	return Display{}, false
}

// microphone picks the microphone to record, as (device id, display name). An empty want
// takes whatever macOS is set to, which is what most people mean. A named one is matched
// the way --app matches a window: case-insensitive substring.
func microphone(want string) (id, name string, err error) {
	want = strings.TrimSpace(want)
	if want == "" {
		name = "system default"
		if c := C.rc_default_microphone(); c != nil {
			name = C.GoString(c)
			C.free(unsafe.Pointer(c))
		}
		return "", name, nil
	}
	mics := Microphones()
	for _, d := range mics {
		if strings.Contains(strings.ToLower(d.Name), strings.ToLower(want)) {
			return d.ID, d.Name, nil
		}
	}
	var known []string
	for _, d := range mics {
		known = append(known, d.Name)
	}
	if len(known) == 0 {
		known = []string{"none"}
	}
	return "", "", fmt.Errorf("no microphone matching %q — available: %s", want, strings.Join(known, ", "))
}

// Warning is something that degraded a recording without failing it.
type Warning string

// RecordOptions are the caller's hooks into a recording.
type RecordOptions struct {
	// Choose asks which window to record when the target is TargetAsk.
	Choose Chooser
	// OnStart is called once recording has actually begun.
	OnStart func(*Plan)
	// Stop blocks until the recording should end. It is handed the pause log; whatever
	// it toggles is cut from the render, not from the capture itself.
	Stop func(*pauses.Log)
	// Warn reports something that degraded the recording without failing it.
	Warn func(Warning)
	// WebcamPreview asks the webcam for live thumbnails, read through Plan.Camera. It
	// adds a throttled output to the camera session; leave it off when nothing shows them.
	WebcamPreview bool
}

// Record records until opts.Stop returns, writing the capture and its sidecars into s.
func Record(s *session.Session, target Target, cfg config.Config, opts RecordOptions) error {
	choose, onStart, stop, warn := opts.Choose, opts.OnStart, opts.Stop, opts.Warn
	plan, err := MakePlan(target, choose)
	if err != nil {
		return err
	}

	// Resolved before the stream is built, so a bad device name fails immediately rather
	// than after a recording has already been made.
	ropts := C.rc_record_opts{
		display_id: C.uint32_t(plan.Display.ID),
		width:      C.uint32_t(plan.CaptureW),
		height:     C.uint32_t(plan.CaptureH),
	}
	var cstrings []*C.char
	defer func() {
		for _, p := range cstrings {
			C.free(unsafe.Pointer(p))
		}
	}()
	cstr := func(s string) *C.char {
		p := C.CString(s)
		cstrings = append(cstrings, p)
		return p
	}
	if cfg.Audio.Microphone {
		id, name, err := microphone(cfg.Audio.Device)
		if err != nil {
			return err
		}
		plan.Microphone = name
		ropts.microphone = true
		if id != "" {
			ropts.microphone_id = cstr(id)
		}
	}

	// rect stays in global points: cursor telemetry is global, and the renderer subtracts
	// this origin from it. ScreenCaptureKit's crop, though, is relative to the display.
	rect := plan.Display.Frame
	var webRect *Rect
	if w := plan.Window; w != nil {
		rect = w.Frame
		ropts.window_id = C.uint32_t(w.ID)
		ropts.crop = true
		ropts.source_rect = C.rc_rect{
			x: C.double(rect.X - plan.Display.Frame.X), y: C.double(rect.Y - plan.Display.Frame.Y),
			w: C.double(rect.W), h: C.double(rect.H),
		}
		// Ask the browser where its page actually is, rather than guessing a fixed crop.
		// Read before recording starts so the layout matches the frames.
		if config.IsBrowser(w.BundleID) {
			if r, ok := WebContentRect(w.PID, w.Frame); ok {
				webRect = &r
			}
		}
	}
	ropts.path = cstr(s.Capture())

	if cfg.Webcam.Enabled {
		cam, err := StartWebcam(cfg.Webcam.Device, s.Camera(), opts.WebcamPreview)
		if err != nil {
			warn(Warning("webcam unavailable, recording screen only: " + err.Error()))
		} else {
			plan.Camera, plan.Webcam = cam, cam.Name
		}
	}

	C.rc_telemetry_start(C.uint32_t(telemetry.PollHz))
	if e := C.rc_record_start(ropts); e != nil {
		tel := C.rc_telemetry_stop()
		C.rc_telemetry_free(tel)
		if plan.Camera != nil {
			_, _ = plan.Camera.Stop()
			_ = os.Remove(s.Camera())
		}
		defer C.free(unsafe.Pointer(e))
		return errors.New(C.GoString(e))
	}
	onStart(plan)
	log := pauses.NewLog(clock.NowNanos)
	stop(log)
	trace("stop requested")
	ps := log.Finish(clock.NowNanos())

	defer watchStop()()
	trace("stopping screen capture")
	var cframes *C.rc_frame
	var nframes C.int
	stopErr := C.rc_record_stop(&cframes, &nframes)
	trace("stopping telemetry")
	ctel := C.rc_telemetry_stop()
	tel, reenabled := telemetryFromC(ctel)
	C.rc_telemetry_free(ctel)
	frameLog := framesFromC(cframes, int(nframes))
	C.free(unsafe.Pointer(cframes))
	if plan.Camera != nil {
		trace("stopping webcam")
		camFrames, err := plan.Camera.Stop()
		trace("webcam stopped")
		if err != nil {
			// Whatever camera.mp4 holds is still usable; the render skips the overlay if
			// it is not.
			warn(Warning("webcam: " + err.Error()))
		}
		if err := writeJSON(s.CameraFrames(), camFrames); err != nil {
			return err
		}
		if _, err := os.Stat(s.Camera()); err == nil {
			if err := session.Restrict(s.Camera()); err != nil {
				return err
			}
		}
	}
	if stopErr != nil {
		defer C.free(unsafe.Pointer(stopErr))
		return errors.New(C.GoString(stopErr))
	}
	if !tel.TapInstalled {
		warn("could not install event tap — clicks were not recorded. Grant Input Monitoring in System Settings › Privacy & Security")
	}
	if reenabled > 0 {
		warn(Warning(fmt.Sprintf("the system paused click capture %d time(s); it was resumed, but clicks in those moments may be missing", reenabled)))
	}

	// Sidecars describe where the cursor went for the whole session; keep them as private
	// as the video itself.
	m := map[string]any{
		"display_w": plan.Display.Width,
		"display_h": plan.Display.Height,
		"capture_w": plan.CaptureW,
		"capture_h": plan.CaptureH,
		"scale":     plan.Scale,
		// Cursor telemetry is global and in points; the renderer subtracts this origin and
		// applies the scale to reach capture pixels.
		"origin_x":   rect.X,
		"origin_y":   rect.Y,
		"source_w":   rect.W,
		"source_h":   rect.H,
		"bundle_id":  nil,
		"app_name":   nil,
		"web_rect":   nil,
		"microphone": nullable(plan.Microphone),
		"webcam":     nullable(plan.Webcam),
		// Mach-ns spans the render cuts out; see the pauses package.
		"pauses": ps,
	}
	if w := plan.Window; w != nil {
		m["bundle_id"] = nullable(w.BundleID)
		m["app_name"] = nullable(w.AppName)
	}
	if webRect != nil {
		m["web_rect"] = map[string]float64{"x": webRect.X, "y": webRect.Y, "w": webRect.W, "h": webRect.H}
	}
	for path, v := range map[string]any{s.Telemetry(): tel, s.Frames(): frameLog, s.Meta(): m} {
		if err := writeJSON(path, v); err != nil {
			return err
		}
	}
	// ScreenCaptureKit writes the capture with the default umask.
	return session.Restrict(s.Capture())
}

// tracing is RECORDO_TRACE=1: step-by-step timings for diagnosing a hang.
var tracing = os.Getenv("RECORDO_TRACE") != ""

func trace(msg string) {
	if tracing {
		fmt.Fprintf(os.Stderr, "  [trace go %s] %s\n", time.Now().Format("15:04:05.000"), msg)
	}
}

// watchStop, when tracing, dumps every goroutine's stack if stopping takes longer than
// every timeout in the stop path combined, then exits. Call the returned func when done.
func watchStop() func() {
	if !tracing {
		return func() {}
	}
	t := time.AfterFunc(30*time.Second, func() {
		fmt.Fprintln(os.Stderr, "  [trace] stop has taken 30s; goroutine stacks follow")
		_ = pprof.Lookup("goroutine").WriteTo(os.Stderr, 2)
		os.Exit(3)
	})
	return func() { t.Stop() }
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	return session.WritePrivate(path, b)
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func telemetryFromC(t C.rc_telemetry) (*telemetry.Telemetry, int) {
	out := &telemetry.Telemetry{TapInstalled: bool(t.tap_installed), Events: []telemetry.Event{}}
	kinds := []telemetry.EventKind{telemetry.Move, telemetry.Down, telemetry.Up, telemetry.Scroll}
	for _, e := range unsafe.Slice(t.events, int(t.n_events)) {
		out.Events = append(out.Events, telemetry.Event{
			TNs:  clock.MachToNanos(uint64(e.t_mach)),
			Kind: kinds[e.kind],
			X:    float64(e.x),
			Y:    float64(e.y),
		})
	}
	// Two threads append; the camera model needs time order.
	slices.SortStableFunc(out.Events, func(a, b telemetry.Event) int {
		return compareU64(a.TNs, b.TNs)
	})
	return out, int(t.tap_reenabled)
}

func framesFromC(f *C.rc_frame, n int) []frames.Record {
	out := make([]frames.Record, 0, n)
	for _, c := range unsafe.Slice(f, n) {
		var r frames.Record
		if c.has_pts {
			v := uint64(c.pts_ns)
			r.PtsNs = &v
		}
		if c.has_display {
			v := clock.MachToNanos(uint64(c.display_mach))
			r.DisplayTimeNs = &v
		}
		out = append(out, r)
	}
	return out
}

func compareU64(a, b uint64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// Health summarises how well cursor telemetry lines up with captured frames. If the
// median gap is large, the mach-timestamp correlation the whole design rests on is broken.
type Health struct {
	Frames        int
	CursorSamples int
	Clicks        int
	MedianGapMs   float64
	TapInstalled  bool
}

// CheckHealth reads a session's sidecars and reports their alignment.
func CheckHealth(s *session.Session) (Health, error) {
	var tel telemetry.Telemetry
	var frameLog []frames.Record
	for path, v := range map[string]any{s.Telemetry(): &tel, s.Frames(): &frameLog} {
		b, err := os.ReadFile(path)
		if err != nil {
			return Health{}, fmt.Errorf("read %s: %w", path, err)
		}
		if err := json.Unmarshal(b, v); err != nil {
			return Health{}, fmt.Errorf("parse %s: %w", path, err)
		}
	}
	return health(&tel, frameLog), nil
}

func health(tel *telemetry.Telemetry, frameLog []frames.Record) Health {
	var timed []uint64
	for _, f := range frameLog {
		if f.DisplayTimeNs != nil {
			timed = append(timed, *f.DisplayTimeNs)
		}
	}
	// Sampling is deduplicated while the cursor is parked, so one frame can legitimately
	// sit seconds from a sample. The median is what says the streams line up.
	var gaps []float64
	for i := 0; i < len(timed); i += 10 {
		best := math.Inf(1)
		for _, e := range tel.Events {
			best = math.Min(best, math.Abs(float64(int64(e.TNs)-int64(timed[i]))))
		}
		if !math.IsInf(best, 1) {
			gaps = append(gaps, best/1e6)
		}
	}
	slices.Sort(gaps)
	median := math.NaN()
	if len(gaps) > 0 {
		median = gaps[len(gaps)/2]
	}
	return Health{
		Frames:        len(timed),
		CursorSamples: tel.Count(telemetry.Move),
		Clicks:        tel.Count(telemetry.Down),
		MedianGapMs:   median,
		TapInstalled:  tel.TapInstalled,
	}
}
