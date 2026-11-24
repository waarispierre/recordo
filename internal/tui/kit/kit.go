// Package kit is what the TUI shell and the feature slices' tabs share: the Tab interface
// the shell talks to, the Host interface tabs talk back through, and the building blocks
// more than one tab draws with — styles, the scrolling list, popups.
//
// It imports no slice and not the shell, so every slice can import it without a cycle.
package kit

import (
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// Overrides are per-run settings that would otherwise come from CLI flags, so a bare
// `recordo --zoom 30` still honours --zoom when a recording is started from the Record
// tab.
type Overrides struct {
	Zoom   *float64
	Mic    *bool
	Webcam *bool
	// Seconds stops a recording after this much recorded time; 0 means no limit.
	Seconds  uint
	NoRender bool
	NoOpen   bool
}

// JobKind is a background job the shell runs on a tab's behalf. Recording is
// deliberately not one: it belongs to the record tab, which draws its own panel.
type JobKind int

const (
	JobRender JobKind = iota
	JobCompress
)

// Host is the shell, as a tab sees it. Every method is called from the tab's Update, on
// the program's goroutine.
type Host interface {
	// SetStatus replaces the status bar's message.
	SetStatus(s string)
	// ShowReport pops up a finished job's report until the next keypress.
	ShowReport(lines ...string)
	// StartJob runs a render or compress on a goroutine, showing its progress in place of
	// the tabs until it finishes; then the report pops up (or "failed: …") and every tab
	// is refreshed. run's progress may be called from any goroutine.
	StartJob(kind JobKind, status string, run func(progress func(done, total int)) (string, error)) tea.Cmd
	// Background runs fn on a goroutine the app waits for before exiting, delivering its
	// result as a message.
	Background(fn func() tea.Msg) tea.Cmd
	// Changed says the recordings on disk changed; every tab is refreshed.
	Changed()
	// Farewell is printed once the terminal is back, for something the user would
	// otherwise never see.
	Farewell(text string)
	// Fail ends the app with err.
	Fail(err error) tea.Cmd
}

// Tab is one of the app's tabs, owned by a feature slice. Embed Base for the optional
// parts.
type Tab interface {
	Title() string
	// Update handles a message. Keys only arrive while the tab is active or capturing;
	// every other message is broadcast to all tabs.
	Update(msg tea.Msg) tea.Cmd
	// Draw draws the tab's panel into area.
	Draw(c *lipgloss.Canvas, area Rect)
	// Keys are the hints shown in the status bar while the tab is active.
	Keys() []key.Binding

	// Modal reports a popup (an edit, a confirmation) that takes every key, including
	// the ones that would otherwise switch tabs or quit.
	Modal() bool
	// DrawPopup draws that popup over the whole screen.
	DrawPopup(c *lipgloss.Canvas, screen Rect)
	// Capturing reports that the tab has taken over the screen (a countdown or a
	// recording): its panel is shown, and it gets every key, whichever tab is active.
	Capturing() bool
	// Badge is drawn after the tab titles, e.g. a live recording's pulse.
	Badge() string
	// Animating asks for a TickMsg every 100ms.
	Animating() bool
	// Refresh re-reads whatever on disk the tab shows.
	Refresh()
	// Stop asks the tab's background work to finish, when the app is ending.
	Stop()
	// Close releases anything still held once that work has finished.
	Close()
}

// Base is the no-op half of Tab.
type Base struct{}

func (Base) Modal() bool                      { return false }
func (Base) DrawPopup(*lipgloss.Canvas, Rect) {}
func (Base) Capturing() bool                  { return false }
func (Base) Badge() string                    { return "" }
func (Base) Animating() bool                  { return false }
func (Base) Refresh()                         {}
func (Base) Stop()                            {}
func (Base) Close()                           {}

// TickMsg arrives every 100ms while anything is animating.
type TickMsg struct{}

// LayoutMsg is the terminal's size, and how many rows the panel between the tab and
// status bars has.
type LayoutMsg struct {
	Width, Height int
	PanelRows     int
}

// DefaultStatus is the status bar's resting message.
const DefaultStatus = "j/k move · enter select · tab switch · q quit"
