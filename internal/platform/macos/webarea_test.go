package macos

import "testing"

func TestWebAreaOutsideItsWindowIsRejected(t *testing.T) {
	window := Rect{100, 100, 800, 600}
	if !insideWindow(Rect{100, 180, 800, 520}, window) {
		t.Error("page inside the window should pass")
	}
	if insideWindow(Rect{100, 180, 800, 900}, window) {
		t.Error("page running past the window should fail")
	}
}
