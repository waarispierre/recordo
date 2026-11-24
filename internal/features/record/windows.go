package record

import (
	"cmp"
	"fmt"
	"github.com/waarispierre/recordo/internal/platform/macos"
	"slices"
	"strings"
)

// systemApps own windows that sit on layer 0 but are never worth recording.
var systemApps = []string{
	"com.apple.dock",
	"com.apple.controlcenter",
	"com.apple.notificationcenterui",
	"com.apple.wallpaper",
	"com.apple.windowmanager",
	"com.apple.screencaptureui",
}

// Capturable is the windows worth offering as a recording target, grouped by app with
// the largest window first within each.
//
// ScreenCaptureKit reports everything the window server knows about, which includes the
// Dock, Control Center, the desktop backstop, autofill popovers and assorted agents.
// Normal application windows sit on layer 0; anything else is a system overlay.
func Capturable(windows []macos.Window) []macos.Window {
	var out []macos.Window
	for _, w := range windows {
		if w.Layer != 0 || !w.OnScreen || w.Frame.W < 400 || w.Frame.H < 300 {
			continue
		}
		if w.BundleID == "" || slices.Contains(systemApps, strings.ToLower(w.BundleID)) {
			continue
		}
		out = append(out, w)
	}
	slices.SortStableFunc(out, func(a, b macos.Window) int {
		return cmp.Or(cmp.Compare(a.AppName, b.AppName), cmp.Compare(area(b), area(a)))
	})
	return out
}

func area(w macos.Window) float64 { return w.Frame.W * w.Frame.H }

// Label is how a window is shown to a person.
func Label(w macos.Window) string {
	app := w.AppName
	if app == "" {
		app = "?"
	}
	title := ""
	if w.Title != "" {
		title = " — " + w.Title
	}
	return fmt.Sprintf("%s%s (%dx%d)", app, title, int(w.Frame.W), int(w.Frame.H))
}

// Visible keeps the windows whose centre is on a display. Windows macOS has moved out of
// view — on another Space, stashed by Stage Manager, or left behind by a disconnected
// monitor — are parked past the screen edge with their centre far off it, so they are
// never offered. A window that merely overhangs an edge is still visible, and is offered;
// MakePlan explains why it cannot be recorded as it is.
func Visible(windows []macos.Window, displays []macos.Display) []macos.Window {
	var out []macos.Window
	for _, w := range windows {
		if _, ok := displayAt(displays, w.Frame.X+w.Frame.W/2, w.Frame.Y+w.Frame.H/2); ok {
			out = append(out, w)
		}
	}
	return out
}

// displayAt is the display containing a point.
func displayAt(displays []macos.Display, x, y float64) (macos.Display, bool) {
	for _, d := range displays {
		if x >= d.Frame.X && y >= d.Frame.Y && x < d.Frame.X+d.Frame.W && y < d.Frame.Y+d.Frame.H {
			return d, true
		}
	}
	return macos.Display{}, false
}

// Recordable is the windows worth offering: capturable and visible.
func Recordable(c *macos.Content) []macos.Window { return Visible(Capturable(c.Windows), c.Displays) }

// LargestForApp picks the window to record for --app: a case-insensitive substring match
// on the app name. An app usually has several windows — panels, inspectors, popovers —
// and the largest is almost always the one worth recording.
func LargestForApp(windows []macos.Window, name string) (macos.Window, bool) {
	var best macos.Window
	found := false
	want := strings.ToLower(name)
	for _, w := range windows {
		if strings.Contains(strings.ToLower(w.AppName), want) && (!found || area(w) > area(best)) {
			best, found = w, true
		}
	}
	return best, found
}
