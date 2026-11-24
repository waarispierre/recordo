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

func TestParkedWindowsAreNotOffered(t *testing.T) {
	// From a real session: the laptop screen alone, with Safari's larger window parked
	// past the bottom-right corner and the one actually on screen smaller.
	c := &Content{
		Displays: []Display{{ID: 1, Width: 1512, Height: 982, Frame: Rect{0, 0, 1512, 982}}},
		Windows: []Window{
			{ID: 10633, OnScreen: true, AppName: "Safari", BundleID: "com.apple.Safari", Frame: Rect{1511, 930, 1496, 932}},
			{ID: 9766, OnScreen: true, AppName: "Safari", BundleID: "com.apple.Safari", Frame: Rect{8, 41, 744, 932}},
		},
	}
	if w, ok := LargestForApp(c.Recordable(), "safari"); !ok || w.ID != 9766 {
		t.Fatalf("got %v %v, want the visible window 9766", w.ID, ok)
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

func TestWindowIsRecordedOnTheDisplayItIsOn(t *testing.T) {
	laptop := Display{ID: 1, Width: 1512, Height: 982, Frame: Rect{0, 0, 1512, 982}}
	monitor := Display{ID: 2, Width: 1920, Height: 1080, Frame: Rect{1512, -98, 1920, 1080}}
	ds := []Display{laptop, monitor}
	if d, ok := displayFor(ds, Rect{0, 25, 1512, 957}); !ok || d.ID != 1 {
		t.Errorf("window on the laptop: got %v %v", d.ID, ok)
	}
	if d, ok := displayFor(ds, Rect{1520, -73, 1904, 1033}); !ok || d.ID != 2 {
		t.Errorf("window on the monitor: got %v %v", d.ID, ok)
	}
	if _, ok := displayFor(ds, Rect{-1600, 25, 1200, 800}); ok {
		t.Error("window parked on another Space should match no display")
	}
}
