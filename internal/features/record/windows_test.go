package record

import (
	"github.com/waarispierre/recordo/internal/platform/macos"
)

import "testing"

func win(id uint32, app, bundle string, w, h float64) macos.Window {
	return macos.Window{ID: id, OnScreen: true, AppName: app, BundleID: bundle, Frame: macos.Rect{W: w, H: h}}
}

func TestCapturableDropsSystemAndTinyWindowsAndSorts(t *testing.T) {
	overlay := win(5, "Safari", "com.apple.Safari", 900, 700)
	overlay.Layer = 3
	offscreen := win(6, "Safari", "com.apple.Safari", 900, 700)
	offscreen.OnScreen = false
	got := Capturable([]macos.Window{
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
	c := &macos.Content{
		Displays: []macos.Display{{ID: 1, Width: 1512, Height: 982, Frame: rect(0, 0, 1512, 982)}},
		Windows: []macos.Window{
			{ID: 10633, OnScreen: true, AppName: "Safari", BundleID: "com.apple.Safari", Frame: rect(1511, 930, 1496, 932)},
			{ID: 9766, OnScreen: true, AppName: "Safari", BundleID: "com.apple.Safari", Frame: rect(8, 41, 744, 932)},
		},
	}
	if w, ok := LargestForApp(Recordable(c), "safari"); !ok || w.ID != 9766 {
		t.Fatalf("got %v %v, want the visible window 9766", w.ID, ok)
	}
}

func TestAWindowOverhangingTheEdgeIsStillOffered(t *testing.T) {
	c := &macos.Content{
		Displays: []macos.Display{{ID: 1, Width: 1512, Height: 982, Frame: rect(0, 0, 1512, 982)}},
		Windows: []macos.Window{
			{ID: 1, OnScreen: true, AppName: "Safari", BundleID: "com.apple.Safari", Frame: rect(20, 40, 1500, 960)},
		},
	}
	if len(Recordable(c)) != 1 {
		t.Fatal("a window a few points past the edge is visible and should be offered")
	}
	if _, ok := displayFor(c.Displays, c.Windows[0].Frame); ok {
		t.Fatal("but it is not wholly on the display, so recording it needs a clear error")
	}
}

func TestLargestForAppMatchesBySubstring(t *testing.T) {
	ws := []macos.Window{
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
	if got := Label(w); got != "Safari — Docs (1512x982)" {
		t.Fatalf("got %q", got)
	}
}

func TestWindowIsRecordedOnTheDisplayItIsOn(t *testing.T) {
	laptop := macos.Display{ID: 1, Width: 1512, Height: 982, Frame: rect(0, 0, 1512, 982)}
	monitor := macos.Display{ID: 2, Width: 1920, Height: 1080, Frame: rect(1512, -98, 1920, 1080)}
	ds := []macos.Display{laptop, monitor}
	if d, ok := displayFor(ds, rect(0, 25, 1512, 957)); !ok || d.ID != 1 {
		t.Errorf("window on the laptop: got %v %v", d.ID, ok)
	}
	if d, ok := displayFor(ds, rect(1520, -73, 1904, 1033)); !ok || d.ID != 2 {
		t.Errorf("window on the monitor: got %v %v", d.ID, ok)
	}
	if _, ok := displayFor(ds, rect(-1600, 25, 1200, 800)); ok {
		t.Error("window parked on another Space should match no display")
	}
}

func rect(x, y, w, h float64) macos.Rect { return macos.Rect{X: x, Y: y, W: w, H: h} }
