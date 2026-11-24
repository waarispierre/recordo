package macos

/*
#include <stdlib.h>
#include "bridge.h"
*/
import "C"

import (
	"errors"
	"fmt"
	"os"
	"runtime/pprof"
	"slices"
	"time"
	"unsafe"

	"github.com/waarispierre/recordo/internal/domain/frames"
	"github.com/waarispierre/recordo/internal/domain/telemetry"
	"github.com/waarispierre/recordo/internal/platform/clock"
)

// ScreenRecording is what to hand ScreenCaptureKit for one recording.
type ScreenRecording struct {
	DisplayID uint32
	// WindowID 0 records the whole display.
	WindowID uint32
	// Crop, when set, is the region to keep, relative to the display's own origin.
	Crop *Rect
	// Width and Height are the output size in pixels.
	Width, Height uint32
	// Microphone records voice over; MicrophoneID "" means the system default.
	Microphone   bool
	MicrophoneID string
	// Path is where capture.mp4 is written.
	Path string
}

// StartScreenRecording starts ScreenCaptureKit writing r.Path. One recording at a time.
func StartScreenRecording(r ScreenRecording) error {
	opts := C.rc_record_opts{
		display_id: C.uint32_t(r.DisplayID),
		window_id:  C.uint32_t(r.WindowID),
		width:      C.uint32_t(r.Width),
		height:     C.uint32_t(r.Height),
		microphone: C.bool(r.Microphone),
	}
	if c := r.Crop; c != nil {
		opts.crop = true
		opts.source_rect = C.rc_rect{x: C.double(c.X), y: C.double(c.Y), w: C.double(c.W), h: C.double(c.H)}
	}
	if r.MicrophoneID != "" {
		opts.microphone_id = C.CString(r.MicrophoneID)
		defer C.free(unsafe.Pointer(opts.microphone_id))
	}
	opts.path = C.CString(r.Path)
	defer C.free(unsafe.Pointer(opts.path))
	if e := C.rc_record_start(opts); e != nil {
		defer C.free(unsafe.Pointer(e))
		return errors.New(C.GoString(e))
	}
	return nil
}

// StopScreenRecording stops the recording, waits for capture.mp4 to be finalised, and
// returns the frame log. The log is returned even alongside an error.
func StopScreenRecording() ([]frames.Record, error) {
	var cframes *C.rc_frame
	var n C.int
	e := C.rc_record_stop(&cframes, &n)
	defer C.free(unsafe.Pointer(cframes))
	out := make([]frames.Record, 0, int(n))
	for _, c := range unsafe.Slice(cframes, int(n)) {
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
	if e != nil {
		defer C.free(unsafe.Pointer(e))
		return out, errors.New(C.GoString(e))
	}
	return out, nil
}

// StartTelemetry starts the cursor poller and the click event tap.
func StartTelemetry() { C.rc_telemetry_start(C.uint32_t(telemetry.PollHz)) }

// StopTelemetry stops both and returns what they saw, in time order, plus how many times
// macOS disabled the tap mid-recording (it is re-armed each time).
func StopTelemetry() (*telemetry.Telemetry, int) {
	t := C.rc_telemetry_stop()
	defer C.rc_telemetry_free(t)
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
		switch {
		case a.TNs < b.TNs:
			return -1
		case a.TNs > b.TNs:
			return 1
		}
		return 0
	})
	return out, int(t.tap_reenabled)
}

// DefaultMicrophone is the name of the microphone macOS is set to, or "".
func DefaultMicrophone() string {
	c := C.rc_default_microphone()
	if c == nil {
		return ""
	}
	defer C.free(unsafe.Pointer(c))
	return C.GoString(c)
}

// Tracing is RECORDO_TRACE=1: step-by-step timings for diagnosing a hang in the capture
// path. The Objective-C side reads the same variable.
var Tracing = os.Getenv("RECORDO_TRACE") != ""

// Trace prints one step when tracing.
func Trace(msg string) {
	if Tracing {
		fmt.Fprintf(os.Stderr, "  [trace go %s] %s\n", time.Now().Format("15:04:05.000"), msg)
	}
}

// WatchStop, when tracing, dumps every goroutine's stack if stopping takes longer than
// every timeout in the stop path combined, then exits. Call the returned func when done.
func WatchStop() func() {
	if !Tracing {
		return func() {}
	}
	t := time.AfterFunc(30*time.Second, func() {
		fmt.Fprintln(os.Stderr, "  [trace] stop has taken 30s; goroutine stacks follow")
		_ = pprof.Lookup("goroutine").WriteTo(os.Stderr, 2)
		os.Exit(3)
	})
	return func() { t.Stop() }
}
