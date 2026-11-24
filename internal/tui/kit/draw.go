package kit

import (
	"image/color"
	"strings"

	"charm.land/bubbles/v2/textinput"
	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// Colours, named after the ratatui ones the Rust version used.
var (
	Black    color.Color = lipgloss.Black
	Red      color.Color = lipgloss.Red
	Green    color.Color = lipgloss.Green
	Yellow   color.Color = lipgloss.Yellow
	Magenta  color.Color = lipgloss.Magenta
	Cyan     color.Color = lipgloss.Cyan
	Gray     color.Color = lipgloss.White
	DarkGray color.Color = lipgloss.BrightBlack
	White    color.Color = lipgloss.BrightWhite
)

var (
	Plain     = lipgloss.NewStyle()
	Dim       = Plain.Foreground(DarkGray)
	Bold      = Plain.Bold(true)
	Highlight = Plain.Foreground(Black).Background(Cyan)
)

// RGB is a true colour.
func RGB(r, g, b uint8) color.Color { return color.RGBA{r, g, b, 0xff} }

// Rect is an area of the screen in cells.
type Rect struct{ X, Y, W, H int }

func (r Rect) Inner() Rect { return Rect{r.X + 1, r.Y + 1, max(r.W-2, 0), max(r.H-2, 0)} }
func (r Rect) Bottom() int { return r.Y + r.H }
func (r Rect) Row(i int) Rect {
	return Rect{r.X, r.Y + i, r.W, 1}
}

// Put clears r and draws s into it, clipped to r — ratatui's Clear followed by a
// Paragraph.
func Put(c *lipgloss.Canvas, r Rect, s string) {
	if r.W <= 0 || r.H <= 0 {
		return
	}
	uv.NewStyledString(s).Draw(c, uv.Rect(r.X, r.Y, r.W, r.H))
}

// DrawBlock clears r, draws a bordered box with title on its top edge, and returns the
// area inside the border.
func DrawBlock(c *lipgloss.Canvas, r Rect, title string, border lipgloss.Style) Rect {
	Put(c, r, "")
	if r.W < 2 || r.H < 2 {
		return Rect{}
	}
	title = ansi.Truncate(title, r.W-2, "")
	tw := ansi.StringWidth(title)
	Put(c, r.Row(0), border.Render("┌")+title+border.Render(strings.Repeat("─", r.W-2-tw)+"┐"))
	Put(c, r.Row(r.H-1), border.Render("└"+strings.Repeat("─", r.W-2)+"┘"))
	for y := 1; y < r.H-1; y++ {
		Put(c, Rect{r.X, r.Y + y, 1, 1}, border.Render("│"))
		Put(c, Rect{r.X + r.W - 1, r.Y + y, 1, 1}, border.Render("│"))
	}
	return r.Inner()
}

// Wrap word-wraps each line to width, as a ratatui Paragraph with Wrap does.
func Wrap(lines []string, width int) string {
	if width <= 0 {
		return ""
	}
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		out = append(out, lipgloss.Wrap(l, width, ""))
	}
	return strings.Join(out, "\n")
}

// Centered is a percentage of area, centred in it.
func Centered(percentX, percentY int, area Rect) Rect {
	w, h := area.W*percentX/100, area.H*percentY/100
	return Rect{area.X + (area.W-w)/2, area.Y + (area.H-h)/2, w, h}
}

// List is a list's selection and scroll position. Selected is -1 for none.
type List struct {
	Selected int
	Offset   int
}

// Get is the selected index, if any.
func (l *List) Get() (int, bool) { return l.Selected, l.Selected >= 0 }

// Move moves the selection by delta through n items, wrapping at either end.
func (l *List) Move(delta, n int) {
	if n == 0 {
		return
	}
	cur := max(l.Selected, 0)
	l.Selected = ((cur+delta)%n + n) % n
}

// Scroll keeps the selection inside rows visible rows, the way ratatui's ListState
// does: the window only moves once the selection reaches its edge.
func (l *List) Scroll(rows int) {
	rows = max(rows, 1)
	if l.Selected < 0 {
		l.Offset = 0
		return
	}
	if l.Selected >= l.Offset+rows {
		l.Offset = l.Selected - rows + 1
	}
	if l.Selected < l.Offset {
		l.Offset = l.Selected
	}
}

// Draw draws items in a bordered list, the selected one highlighted behind a "❯ ". With
// nothing selected there is no marker column, as in ratatui.
func (l List) Draw(c *lipgloss.Canvas, r Rect, title string, items []string) {
	inner := DrawBlock(c, r, title, Plain)
	for i := l.Offset; i < len(items) && i-l.Offset < inner.H; i++ {
		row := inner.Row(i - l.Offset)
		switch {
		case l.Selected < 0:
			Put(c, row, items[i])
		case i == l.Selected:
			text := ansi.Truncate("❯ "+ansi.Strip(items[i]), row.W, "")
			text += strings.Repeat(" ", max(row.W-ansi.StringWidth(text), 0))
			Put(c, row, Highlight.Render(text))
		default:
			Put(c, row, "  "+items[i])
		}
	}
}

// NewInput is the edit popup's text field, sized for a screen width wide.
func NewInput(screenWidth int) textinput.Model {
	in := textinput.New()
	in.Prompt = "› "
	s := in.Styles()
	s.Focused.Prompt = Plain.Foreground(Cyan)
	s.Focused.Text = Bold
	s.Cursor.Color = Cyan
	s.Cursor.Blink = false
	in.SetStyles(s)
	in.SetWidth(max(screenWidth*60/100-6, 10))
	return in
}

// DrawEditPopup asks for a value: a hint, then the input.
func DrawEditPopup(c *lipgloss.Canvas, screen Rect, title, hint string, input textinput.Model) {
	inner := DrawBlock(c, Centered(60, 30, screen), " "+title+" ", Plain.Foreground(Cyan))
	Put(c, inner, Wrap([]string{Dim.Render(hint), ""}, inner.W)+"\n"+input.View())
}

// DrawConfirmPopup asks before anything irreversible: lines spell out what goes and how
// much it frees.
func DrawConfirmPopup(c *lipgloss.Canvas, screen Rect, title string, lines []string) {
	lines = append(lines, "", Plain.Foreground(Red).Render("y")+Dim.Render(" yes · any other key cancels"))
	inner := DrawBlock(c, Centered(56, 34, screen), title, Plain.Foreground(Red))
	Put(c, inner, Wrap(lines, inner.W))
}

// DrawReportPopup shows a finished job's report.
func DrawReportPopup(c *lipgloss.Canvas, screen Rect, report string) {
	inner := DrawBlock(c, Centered(64, 50, screen), " done — press any key ", Plain.Foreground(Green))
	Put(c, inner, Wrap(strings.Split(report, "\n"), inner.W))
}
