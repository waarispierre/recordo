package tui

import (
	"image/color"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/progress"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	"charm.land/lipgloss/v2"
)

// Colours, named after the ratatui ones the Rust version used.
var (
	black    color.Color = lipgloss.Black
	red      color.Color = lipgloss.Red
	green    color.Color = lipgloss.Green
	yellow   color.Color = lipgloss.Yellow
	magenta  color.Color = lipgloss.Magenta
	cyan     color.Color = lipgloss.Cyan
	gray     color.Color = lipgloss.White
	darkGray color.Color = lipgloss.BrightBlack
	white    color.Color = lipgloss.BrightWhite
)

var (
	plain     = lipgloss.NewStyle()
	dim       = plain.Foreground(darkGray)
	bold      = plain.Bold(true)
	highlight = plain.Foreground(black).Background(cyan)
)

type keyMap struct {
	quit, up, down, next, prev, tab1, tab2, tab3 key.Binding
	enter                                        key.Binding
	preview                                      key.Binding
	render, compress, name, dropRaw, del         key.Binding
	// While counting down or recording.
	pause, stop, abort key.Binding
	yes                key.Binding
	// Help-only variants of enter, one per tab.
	enterRecord, enterEdit, enterOpen key.Binding
}

func newKeyMap() keyMap {
	b := func(keys []string, k, desc string) key.Binding {
		return key.NewBinding(key.WithKeys(keys...), key.WithHelp(k, desc))
	}
	ks := func(keys ...string) []string { return keys }
	return keyMap{
		quit:        b(ks("q", "esc", "ctrl+c"), "q", "quit"),
		up:          b(ks("k", "up"), "k", "up"),
		down:        b(ks("j", "down"), "j", "down"),
		next:        b(ks("tab", "l"), "tab", "switch"),
		prev:        b(ks("shift+tab", "h"), "shift+tab", "switch back"),
		tab1:        b(ks("1"), "1", "record"),
		tab2:        b(ks("2"), "2", "settings"),
		tab3:        b(ks("3"), "3", "recordings"),
		enter:       b(ks("enter"), "enter", "select"),
		preview:     b(ks("w"), "w", "preview"),
		render:      b(ks("r"), "r", "render"),
		compress:    b(ks("c"), "c", "compress"),
		name:        b(ks("n"), "n", "name"),
		dropRaw:     b(ks("p"), "p", "drop raw"),
		del:         b(ks("d"), "d", "delete"),
		pause:       b(ks("p", "space"), "p/space", "pause"),
		stop:        b(ks("enter", "s"), "enter/s", "stop"),
		abort:       b(ks("q", "esc", "ctrl+c"), "q", "stop and quit"),
		yes:         b(ks("y", "Y"), "y", "yes"),
		enterRecord: b(ks("enter"), "enter", "record"),
		enterEdit:   b(ks("enter"), "enter", "edit"),
		enterOpen:   b(ks("enter"), "enter", "open"),
	}
}

// statusKeys is the key hint shown in the status bar for the current tab.
func (k keyMap) statusKeys(t tab) []key.Binding {
	switch t {
	case tabSettings:
		return []key.Binding{k.enterEdit, k.next, k.quit}
	case tabRecordings:
		return []key.Binding{k.enterOpen, k.render, k.compress, k.name, k.dropRaw, k.del}
	default:
		return []key.Binding{k.enterRecord, k.preview, k.next, k.quit}
	}
}

func newHelp() help.Model {
	h := help.New()
	h.ShortSeparator = " · "
	h.Styles.ShortKey = dim
	h.Styles.ShortDesc = dim
	h.Styles.ShortSeparator = dim
	return h
}

// newBar is the render progress bar: 24 cells of ▰/▱, with the frame count beside it
// rather than a percentage.
func newBar() progress.Model {
	bar := progress.New(
		progress.WithFillCharacters('▰', '▱'),
		progress.WithWidth(24),
		progress.WithoutPercentage(),
	)
	bar.FullColor, bar.EmptyColor = lipgloss.NoColor{}, lipgloss.NoColor{}
	return bar
}

func newSpinner() spinner.Model {
	return spinner.New(spinner.WithSpinner(spinner.Spinner{
		Frames: []string{"▱▱▱", "▰▱▱", "▰▰▱", "▰▰▰", "▱▰▰", "▱▱▰"},
		FPS:    120 * time.Millisecond,
	}))
}

func newInput(width int) textinput.Model {
	in := textinput.New()
	in.Prompt = "› "
	s := in.Styles()
	s.Focused.Prompt = plain.Foreground(cyan)
	s.Focused.Text = bold
	s.Cursor.Color = cyan
	s.Cursor.Blink = false
	in.SetStyles(s)
	in.SetWidth(width)
	return in
}
