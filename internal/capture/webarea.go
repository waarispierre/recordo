package capture

/*
#include "bridge.h"
*/
import "C"

// AccessibilityTrusted reports whether this process may read other apps' accessibility
// trees.
func AccessibilityTrusted() bool { return bool(C.rc_ax_trusted()) }

// WebContentRect is the bounds of the page content for the window whose frame is window,
// in screen points.
//
// The window frame is required rather than optional: an app commonly has several windows,
// including ones parked on other Spaces, and picking the largest web area across all of
// them can silently return a rect belonging to a window that is not being recorded.
//
// ok is false when Accessibility permission is missing, the app is not a browser, or no
// matching web area is found.
func WebContentRect(pid int32, window Rect) (Rect, bool) {
	var out C.rc_rect
	cw := C.rc_rect{x: C.double(window.X), y: C.double(window.Y), w: C.double(window.W), h: C.double(window.H)}
	if !C.rc_ax_web_area(C.int32_t(pid), cw, &out) {
		return Rect{}, false
	}
	r := rectFromC(out)
	return r, insideWindow(r, window)
}

// insideWindow rejects a web area that is not inside its own window: the tree is stale or
// the wrong window matched, and falling back beats cropping to nonsense.
func insideWindow(r, w Rect) bool {
	return r.W > 1 && r.H > 1 &&
		r.X >= w.X-2 && r.Y >= w.Y-2 &&
		r.X+r.W <= w.X+w.W+2 && r.Y+r.H <= w.Y+w.H+2
}
