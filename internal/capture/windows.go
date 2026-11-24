package capture

import (
	"cmp"
	"fmt"
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
func Capturable(windows []Window) []Window {
	var out []Window
	for _, w := range windows {
		if w.Layer != 0 || !w.OnScreen || w.Frame.W < 400 || w.Frame.H < 300 {
			continue
		}
		if w.BundleID == "" || slices.Contains(systemApps, strings.ToLower(w.BundleID)) {
			continue
		}
		out = append(out, w)
	}
	slices.SortStableFunc(out, func(a, b Window) int {
		return cmp.Or(cmp.Compare(a.AppName, b.AppName), cmp.Compare(area(b), area(a)))
	})
	return out
}

func area(w Window) float64 { return w.Frame.W * w.Frame.H }

// Label is how a window is shown to a person.
func (w Window) Label() string {
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

// Visible keeps the windows that sit wholly on one of the displays. Windows macOS has
// moved out of view — on another Space, stashed by Stage Manager, or left behind by a
// disconnected monitor — report a frame parked past the screen edge, and recording one
// can only produce black, so they are never offered.
func Visible(windows []Window, displays []Display) []Window {
	var out []Window
	for _, w := range windows {
		if _, ok := displayFor(displays, w.Frame); ok {
			out = append(out, w)
		}
	}
	return out
}

// Recordable is the windows worth offering: capturable and visible.
func (c *Content) Recordable() []Window { return Visible(Capturable(c.Windows), c.Displays) }

// LargestForApp picks the window to record for --app: a case-insensitive substring match
// on the app name. An app usually has several windows — panels, inspectors, popovers —
// and the largest is almost always the one worth recording.
func LargestForApp(windows []Window, name string) (Window, bool) {
	var best Window
	found := false
	want := strings.ToLower(name)
	for _, w := range windows {
		if strings.Contains(strings.ToLower(w.AppName), want) && (!found || area(w) > area(best)) {
			best, found = w, true
		}
	}
	return best, found
}
