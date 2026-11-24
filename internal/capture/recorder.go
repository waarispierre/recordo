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
	"slices"
	"strings"
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
		w, ok, err := choose(Capturable(content.Windows))
		if err != nil {
			return nil, err
		}
		if ok {
			window = &w
		}
	case TargetApp:
		w, ok := LargestForApp(content.Windows, target.App)
		if !ok {
			return nil, fmt.Errorf("no window found for %q — try `recordo windows`", target.App)
		}
		window = &w
	case TargetWindow:
		i := slices.IndexFunc(Capturable(content.Windows), func(w Window) bool { return w.ID == target.WindowID })
		if i < 0 {
			return nil, fmt.Errorf("no window with id %d — try `recordo windows`", target.WindowID)
		}
		w := Capturable(content.Windows)[i]
		window = &w
	}

	dw, dh := float64(display.Width), float64(display.Height)
	rect := Rect{0, 0, dw, dh}
	label := fmt.Sprintf("entire display (%.0fx%.0f)", dw, dh)
	var windowID uint32
	if window != nil {
		if !onDisplay(window.Frame, dw, dh) {
			return nil, fmt.Errorf("that window sits outside the %.0fx%.0f display, so it is on another Space. "+
				"Switch to it first, or record the whole display", dw, dh)
		}
		rect, label, windowID = window.Frame, window.Label(), window.ID
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

// onDisplay rejects windows parked on another Space: their reported frame is not
// trustworthy and sits outside the display, which would silently crop to an empty region
// and record pure black.
func onDisplay(f Rect, dw, dh float64) bool {
	return f.X >= 0 && f.Y >= 0 && f.X+f.W <= dw+1 && f.Y+f.H <= dh+1
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

// Record records until stop returns, writing the capture and its sidecars into s.
//
// stop is handed the recording's pause log; whatever it toggles is cut from the render,
// not from the capture itself. onStart is called once recording has actually begun.
func Record(s *session.Session, target Target, cfg config.Config, choose Chooser,
	onStart func(*Plan), stop func(*pauses.Log), warn func(Warning)) error {
	plan, err := MakePlan(target, choose)
	if err != nil {
		return err
	}

	// Resolved before the stream is built, so a bad device name fails immediately rather
	// than after a recording has already been made.
	opts := C.rc_record_opts{
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
		opts.microphone = true
		if id != "" {
			opts.microphone_id = cstr(id)
		}
	}

	rect := Rect{0, 0, float64(plan.Display.Width), float64(plan.Display.Height)}
	var webRect *Rect
	if w := plan.Window; w != nil {
		rect = w.Frame
		opts.window_id = C.uint32_t(w.ID)
		opts.crop = true
		opts.source_rect = C.rc_rect{x: C.double(rect.X), y: C.double(rect.Y), w: C.double(rect.W), h: C.double(rect.H)}
		// Ask the browser where its page actually is, rather than guessing a fixed crop.
		// Read before recording starts so the layout matches the frames.
		if config.IsBrowser(w.BundleID) {
			if r, ok := WebContentRect(w.PID, w.Frame); ok {
				webRect = &r
			}
		}
	}
	opts.path = cstr(s.Capture())

	if cfg.Webcam.Enabled {
		warn("webcam not yet supported by the Go build — recording screen only")
	}

	C.rc_telemetry_start(C.uint32_t(telemetry.PollHz))
	if e := C.rc_record_start(opts); e != nil {
		tel := C.rc_telemetry_stop()
		C.rc_telemetry_free(tel)
		defer C.free(unsafe.Pointer(e))
		return errors.New(C.GoString(e))
	}
	onStart(plan)
	log := pauses.NewLog(clock.NowNanos)
	stop(log)
	ps := log.Finish(clock.NowNanos())

	var cframes *C.rc_frame
	var nframes C.int
	stopErr := C.rc_record_stop(&cframes, &nframes)
	ctel := C.rc_telemetry_stop()
	tel, reenabled := telemetryFromC(ctel)
	C.rc_telemetry_free(ctel)
	frameLog := framesFromC(cframes, int(nframes))
	C.free(unsafe.Pointer(cframes))
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
		b, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			return fmt.Errorf("encode %s: %w", path, err)
		}
		if err := session.WritePrivate(path, b); err != nil {
			return err
		}
	}
	// ScreenCaptureKit writes the capture with the default umask.
	return session.Restrict(s.Capture())
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
