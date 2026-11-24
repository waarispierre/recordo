// Package record is the record slice: decide what to capture, record it with cursor
// telemetry and (optionally) the webcam, write the sidecars, and check the result lines up.
package record

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"slices"
	"strings"

	"github.com/waarispierre/recordo/internal/domain/config"
	"github.com/waarispierre/recordo/internal/domain/frames"
	"github.com/waarispierre/recordo/internal/domain/pauses"
	"github.com/waarispierre/recordo/internal/domain/session"
	"github.com/waarispierre/recordo/internal/domain/telemetry"
	"github.com/waarispierre/recordo/internal/platform/clock"
	"github.com/waarispierre/recordo/internal/platform/macos"
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
type Chooser func(windows []macos.Window) (w macos.Window, ok bool, err error)

// Plan is what will be recorded, resolved before anything starts.
type Plan struct {
	Window   *macos.Window // nil records the whole display
	Display  macos.Display
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
	Camera *macos.Webcam
}

// MakePlan resolves what will be recorded, without starting anything.
func MakePlan(target Target, choose Chooser) (*Plan, error) {
	content, err := macos.ShareableContent()
	if err != nil {
		return nil, err
	}
	if len(content.Displays) == 0 {
		return nil, errors.New("no displays found")
	}
	display := content.Displays[0]

	var window *macos.Window
	switch target.Kind {
	case TargetAsk:
		w, ok, err := choose(Recordable(content))
		if err != nil {
			return nil, err
		}
		if ok {
			window = &w
		}
	case TargetApp:
		w, ok := LargestForApp(Recordable(content), target.App)
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
		i := slices.IndexFunc(all, func(w macos.Window) bool { return w.ID == target.WindowID })
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
					"Move it fully onto one screen, or record the whole display", Label(*window), f)
			}
			return nil, fmt.Errorf("%s is not on screen (%s), so it is on another Space or hidden. "+
				"Switch to it first, or record the whole display", Label(*window), f)
		}
		display, label, windowID = d, Label(*window), window.ID
	} else {
		label = fmt.Sprintf("entire display (%dx%d)", display.Width, display.Height)
	}
	rect := display.Frame
	if window != nil {
		rect = window.Frame
	}

	s, err := macos.PointPixelScale(display.ID, windowID)
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
func displayFor(displays []macos.Display, f macos.Rect) (macos.Display, bool) {
	for _, d := range displays {
		if f.X >= d.Frame.X && f.Y >= d.Frame.Y &&
			f.X+f.W <= d.Frame.X+d.Frame.W+1 && f.Y+f.H <= d.Frame.Y+d.Frame.H+1 {
			return d, true
		}
	}
	return macos.Display{}, false
}

// microphone picks the microphone to record, as (device id, display name). An empty want
// takes whatever macOS is set to, which is what most people mean. A named one is matched
// the way --app matches a window: case-insensitive substring.
func microphone(want string) (id, name string, err error) {
	want = strings.TrimSpace(want)
	if want == "" {
		name = macos.DefaultMicrophone()
		if name == "" {
			name = "system default"
		}
		return "", name, nil
	}
	mics := macos.Microphones()
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

	rec := macos.ScreenRecording{
		DisplayID: plan.Display.ID,
		Width:     plan.CaptureW,
		Height:    plan.CaptureH,
		Path:      s.Capture(),
	}
	// Resolved before the stream is built, so a bad device name fails immediately rather
	// than after a recording has already been made.
	if cfg.Audio.Microphone {
		id, name, err := microphone(cfg.Audio.Device)
		if err != nil {
			return err
		}
		plan.Microphone = name
		rec.Microphone, rec.MicrophoneID = true, id
	}

	// rect stays in global points: cursor telemetry is global, and the renderer subtracts
	// this origin from it. ScreenCaptureKit's crop, though, is relative to the display.
	rect := plan.Display.Frame
	var webRect *macos.Rect
	if w := plan.Window; w != nil {
		rect = w.Frame
		rec.WindowID = w.ID
		rec.Crop = &macos.Rect{X: rect.X - plan.Display.Frame.X, Y: rect.Y - plan.Display.Frame.Y, W: rect.W, H: rect.H}
		// Ask the browser where its page actually is, rather than guessing a fixed crop.
		// Read before recording starts so the layout matches the frames.
		if config.IsBrowser(w.BundleID) {
			if r, ok := macos.WebContentRect(w.PID, w.Frame); ok {
				webRect = &r
			}
		}
	}

	if cfg.Webcam.Enabled {
		cam, err := macos.StartWebcam(cfg.Webcam.Device, s.Camera(), opts.WebcamPreview)
		if err != nil {
			warn(Warning("webcam unavailable, recording screen only: " + err.Error()))
		} else {
			plan.Camera, plan.Webcam = cam, cam.Name
		}
	}

	macos.StartTelemetry()
	if err := macos.StartScreenRecording(rec); err != nil {
		macos.StopTelemetry()
		if plan.Camera != nil {
			_, _ = plan.Camera.Stop()
			_ = os.Remove(s.Camera())
		}
		return err
	}
	onStart(plan)
	log := pauses.NewLog(clock.NowNanos)
	stop(log)
	macos.Trace("stop requested")
	ps := log.Finish(clock.NowNanos())

	defer macos.WatchStop()()
	macos.Trace("stopping screen capture")
	frameLog, stopErr := macos.StopScreenRecording()
	macos.Trace("stopping telemetry")
	tel, reenabled := macos.StopTelemetry()
	if plan.Camera != nil {
		macos.Trace("stopping webcam")
		camFrames, err := plan.Camera.Stop()
		macos.Trace("webcam stopped")
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
		return stopErr
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
