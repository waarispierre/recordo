// Package capture is everything OS-dependent about recording: ScreenCaptureKit, the
// cursor and click telemetry, the webcam, and the accessibility lookup for browser pages.
//
// The macOS APIs are called from Objective-C files in this directory through a small C
// interface (bridge.h). Only plain C data crosses it, so no Objective-C object lifetime
// leaks into Go.
package capture

/*
#cgo CFLAGS: -x objective-c -fobjc-arc -mmacosx-version-min=15.0
#cgo LDFLAGS: -framework ScreenCaptureKit -framework AVFoundation -framework CoreMedia -framework CoreVideo -framework ApplicationServices -framework CoreGraphics -framework Foundation
#include <stdlib.h>
#include "bridge.h"
*/
import "C"

import (
	"errors"
	"unsafe"
)

// Rect is a screen-space rectangle in points, top-left origin.
type Rect struct{ X, Y, W, H float64 }

func rectFromC(r C.rc_rect) Rect {
	return Rect{float64(r.x), float64(r.y), float64(r.w), float64(r.h)}
}

func goString(s *C.char) string {
	if s == nil {
		return ""
	}
	return C.GoString(s)
}

// Window is one window as ScreenCaptureKit reports it.
type Window struct {
	ID       uint32
	Layer    int64
	OnScreen bool
	Frame    Rect
	PID      int32
	BundleID string // "" when the window has no owning application
	AppName  string
	Title    string
}

// Display is one display, sized in points.
type Display struct {
	ID            uint32
	Width, Height uint32
}

// Content is everything ScreenCaptureKit can currently capture.
type Content struct {
	Windows  []Window
	Displays []Display
}

// ShareableContent asks ScreenCaptureKit what can be captured. It fails without Screen
// Recording permission.
func ShareableContent() (*Content, error) {
	c := C.rc_shareable_content()
	defer C.rc_content_free(c)
	if c.error != nil {
		return nil, errors.New("could not read shareable content — grant Screen Recording permission to your terminal: " + C.GoString(c.error))
	}
	out := &Content{}
	for _, w := range unsafe.Slice(c.windows, int(c.n_windows)) {
		out.Windows = append(out.Windows, Window{
			ID:       uint32(w.id),
			Layer:    int64(w.layer),
			OnScreen: bool(w.on_screen),
			Frame:    rectFromC(w.frame),
			PID:      int32(w.pid),
			BundleID: goString(w.bundle_id),
			AppName:  goString(w.app_name),
			Title:    goString(w.title),
		})
	}
	for _, d := range unsafe.Slice(c.displays, int(c.n_displays)) {
		out.Displays = append(out.Displays, Display{uint32(d.id), uint32(d.width), uint32(d.height)})
	}
	return out, nil
}

// PointPixelScale is how many pixels make a point when capturing the given display, or
// the given window on it when windowID is non-zero.
func PointPixelScale(displayID, windowID uint32) (float64, error) {
	var cerr *C.char
	s := C.rc_point_pixel_scale(C.uint32_t(displayID), C.uint32_t(windowID), &cerr)
	if cerr != nil {
		defer C.free(unsafe.Pointer(cerr))
		return 0, errors.New("build content filter: " + C.GoString(cerr))
	}
	return float64(s), nil
}
