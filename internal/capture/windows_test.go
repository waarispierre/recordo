package capture

import "testing"

func win(id uint32, app, bundle string, w, h float64) Window {
	return Window{ID: id, OnScreen: true, AppName: app, BundleID: bundle, Frame: Rect{W: w, H: h}}
}

func TestCapturableDropsSystemAndTinyWindowsAndSorts(t *testing.T) {
	overlay := win(5, "Safari", "com.apple.Safari", 900, 700)
	overlay.Layer = 3
	offscreen := win(6, "Safari", "com.apple.Safari", 900, 700)
	offscreen.OnScreen = false
	got := Capturable([]Window{
		win(1, "Safari", "com.apple.Safari", 800, 600),
		win(2, "Dock", "com.apple.dock", 2000, 2000),
		win(3, "Arc", "company.thebrowser.Browser", 1200, 800),
		win(4, "Safari", "com.apple.Safari", 1600, 1000),
		win(7, "Popover", "com.example.x", 300, 200),
		win(8, "NoApp", "", 1000, 1000),
		overlay, offscreen,
	})
	var ids []uint32
	for _, w := range got {
		ids = append(ids, w.ID)
	}
	if len(ids) != 3 || ids[0] != 3 || ids[1] != 4 || ids[2] != 1 {
		t.Fatalf("got %v, want [3 4 1]", ids)
	}
}

func TestLargestForAppMatchesBySubstring(t *testing.T) {
	ws := []Window{
		win(1, "Google Chrome", "com.google.Chrome", 800, 600),
		win(2, "Google Chrome", "com.google.Chrome", 1600, 1000),
	}
	if w, ok := LargestForApp(ws, "chrome"); !ok || w.ID != 2 {
		t.Fatalf("got %v %v", w.ID, ok)
	}
	if _, ok := LargestForApp(ws, "safari"); ok {
		t.Fatal("should not match")
	}
}

func TestLabel(t *testing.T) {
	w := win(1, "Safari", "com.apple.Safari", 1512.6, 982)
	w.Title = "Docs"
	if got := w.Label(); got != "Safari — Docs (1512x982)" {
		t.Fatalf("got %q", got)
	}
}

func TestWebAreaOutsideItsWindowIsRejected(t *testing.T) {
	window := Rect{100, 100, 800, 600}
	if !insideWindow(Rect{100, 180, 800, 520}, window) {
		t.Error("page inside the window should pass")
	}
	if insideWindow(Rect{100, 180, 800, 900}, window) {
		t.Error("page running past the window should fail")
	}
}

func TestWindowOnAnotherSpaceIsRejected(t *testing.T) {
	if !onDisplay(Rect{0, 25, 1512, 957}, 1512, 982) {
		t.Error("window on this display should pass")
	}
	if onDisplay(Rect{-1600, 25, 1200, 800}, 1512, 982) {
		t.Error("window parked off-display should fail")
	}
}
