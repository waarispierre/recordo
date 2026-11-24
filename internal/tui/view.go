package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/waarispierre/recordo/internal/tui/kit"
)

// layout splits the screen into the tab bar, the panel and the status bar.
func (m *Model) layout() (top, mid, bot kit.Rect) {
	// Inset, so the backdrop reads as a frame around the app rather than as noise behind
	// it.
	body := kit.Rect{X: 2, Y: 1, W: max(m.width-4, 0), H: max(m.height-2, 0)}
	topH := min(3, body.H)
	botH := min(3, body.H-topH)
	midH := body.H - topH - botH
	top = kit.Rect{X: body.X, Y: body.Y, W: body.W, H: topH}
	mid = kit.Rect{X: body.X, Y: body.Y + topH, W: body.W, H: midH}
	bot = kit.Rect{X: body.X, Y: body.Y + topH + midH, W: body.W, H: botH}
	return top, mid, bot
}

func (m *Model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	v.WindowTitle = "recordo"
	return v
}

func (m *Model) render() string {
	if m.width <= 0 || m.height <= 0 {
		return ""
	}
	c := lipgloss.NewCanvas(m.width, m.height)
	screen := kit.Rect{W: m.width, H: m.height}
	drawBackground(c, screen)

	top, mid, bot := m.layout()
	// Cleared, so the backdrop does not show through a panel's empty interior.
	for _, r := range []kit.Rect{top, mid, bot} {
		kit.Put(c, r, "")
	}

	m.drawTabs(c, top)
	active := m.tabs[m.active]
	switch t := m.capturing(); {
	case t != nil:
		t.Draw(c, mid)
	case m.job != nil:
		m.drawJob(c, mid)
	default:
		active.Draw(c, mid)
	}
	m.drawStatus(c, bot)
	// Last, so it can see what the panel actually drew and stay out of its way.
	drawWordmark(c, mid)

	active.DrawPopup(c, screen)
	if m.reporting {
		kit.DrawReportPopup(c, screen, m.report)
	}
	return c.Render()
}

// drawBackground is a faint diagonal dot texture behind the whole app — drawn first, so
// every panel drawn afterwards simply overwrites the cells it occupies. No image support
// is needed (or added): terminals without a graphics protocol can't show one anyway.
func drawBackground(c *lipgloss.Canvas, r kit.Rect) {
	lines := make([]string, r.H)
	row := make([]rune, r.W)
	for y := range r.H {
		for x := range r.W {
			row[x] = ' '
			if (x+y*2)%6 == 0 {
				row[x] = '·'
			}
		}
		lines[y] = string(row)
	}
	kit.Put(c, r, kit.Plain.Foreground(kit.RGB(42, 34, 58)).Render(strings.Join(lines, "\n")))
}

func (m *Model) drawTabs(c *lipgloss.Canvas, r kit.Rect) {
	var b strings.Builder
	b.WriteString(kit.Plain.Foreground(kit.Magenta).Bold(true).Render(" recordo "))
	for i, t := range m.tabs {
		style := kit.Dim
		if i == m.active {
			style = kit.Plain.Foreground(kit.Black).Background(kit.Cyan).Bold(true)
		}
		b.WriteString(style.Render(" " + t.Title() + " "))
		b.WriteString(" ")
	}
	for _, t := range m.tabs {
		b.WriteString(t.Badge())
	}
	kit.Put(c, r.Row(0), b.String())
	if r.H > 1 {
		kit.Put(c, r.Row(r.H-1), strings.Repeat("─", r.W))
	}
}

// drawJob replaces the tab's own content with a status panel while a job is running —
// simpler than threading job state through each tab's drawing, and a job's progress
// matters regardless of which tab the user is looking at.
func (m *Model) drawJob(c *lipgloss.Canvas, r kit.Rect) {
	j := m.job
	inner := kit.DrawBlock(c, r, " working ", kit.Plain)
	var lines []string
	switch j.kind {
	case kit.JobRender:
		// total 0 means the first frame hasn't landed yet, which is a distinct state from
		// 0/N — ffmpeg is still starting up and there is nothing to show.
		if j.total == 0 {
			lines = []string{"rendering", "starting ffmpeg…"}
		} else {
			lines = []string{"rendering", fmt.Sprintf("%s  %d/%d frames",
				m.bar.ViewAs(float64(j.done)/float64(j.total)), j.done, j.total)}
		}
	case kit.JobCompress:
		lines = []string{"compressing", m.spin.View()}
	}
	kit.Put(c, inner, strings.Join(lines, "\n"))
}

func (m *Model) drawStatus(c *lipgloss.Canvas, r kit.Rect) {
	if r.H == 0 {
		return
	}
	kit.Put(c, r.Row(0), strings.Repeat("─", r.W))
	if r.H > 1 {
		kit.Put(c, r.Row(1), kit.Plain.Foreground(kit.White).Render(" "+m.status+" ")+
			"  "+m.help.ShortHelpView(m.tabs[m.active].Keys()))
	}
}

// wordmarkRows is the rows the wordmark occupies: three of block type, a blank, and the
// tagline.
const wordmarkRows = 5

// Letterforms are six pixels tall but drawn in three terminal rows, folded together by the
// same half-block trick the webcam overlay once used. Three rows of whole cells is the
// height that fits here, and at that size solid cells are too coarse to tell an R from a
// D; the half-blocks buy back the vertical detail that makes it read as a word.
var wordmarkGlyphs = [7][6]string{
	{"████ ", "█   █", "█   █", "████ ", "█  █ ", "█   █"}, // R
	{"█████", "█    ", "████ ", "█    ", "█    ", "█████"}, // E
	{" ████", "█    ", "█    ", "█    ", "█    ", " ████"}, // C
	{" ███ ", "█   █", "█   █", "█   █", "█   █", " ███ "}, // O
	{"████ ", "█   █", "█   █", "████ ", "█  █ ", "█   █"}, // R
	{"████ ", "█   █", "█   █", "█   █", "█   █", "████ "}, // D
	{" ███ ", "█   █", "█   █", "█   █", "█   █", " ███ "}, // O
}

const tagline = "screen recordings with a cursor-following camera"

// drawWordmark is a block-type "RECORDO" watermark, centred in area.
//
// Drawn after the panel and only into a block of cells the panel left completely empty.
// Interleaving it with content does not read as a backdrop — against a full settings list
// the letterforms land between the keys and their values and just make both harder to
// read — so when the middle of the screen is occupied the mark stands down entirely
// rather than moving somewhere it fits.
func drawWordmark(c *lipgloss.Canvas, area kit.Rect) {
	var pixelRows [6][]rune
	for r := range 6 {
		parts := make([]string, len(wordmarkGlyphs))
		for i, g := range wordmarkGlyphs {
			parts[i] = g[r]
		}
		pixelRows[r] = []rune(strings.Join(parts, "  "))
	}
	width := max(len(pixelRows[0]), len(tagline))
	if area.W < width || area.H < wordmarkRows {
		return
	}
	x := area.X + (area.W-width)/2
	y, ok := clearBand(c, area, x, width)
	if !ok {
		return
	}

	centre := func(s string) string {
		return strings.Repeat(" ", (width-ansi.StringWidth(s))/2) + s
	}
	mark := kit.Plain.Foreground(kit.RGB(96, 68, 132))
	var lines []string
	for r := 0; r < 6; r += 2 {
		folded := make([]rune, len(pixelRows[r]))
		for i := range folded {
			switch top, bottom := pixelRows[r][i] != ' ', pixelRows[r+1][i] != ' '; {
			case top && bottom:
				folded[i] = '█'
			case top:
				folded[i] = '▀'
			case bottom:
				folded[i] = '▄'
			default:
				folded[i] = ' '
			}
		}
		lines = append(lines, centre(mark.Render(string(folded))))
	}
	lines = append(lines, "", centre(kit.Plain.Foreground(kit.RGB(48, 41, 62)).Render(tagline)))
	kit.Put(c, kit.Rect{X: x, Y: y, W: width, H: wordmarkRows}, strings.Join(lines, "\n"))
}

// clearBand is the top row at which wordmarkRows rows fit in the tallest run of rows the
// panel left untouched across the mark's columns, centred within that run. ok is false
// when no run is tall enough, which is how a full settings list keeps the mark off the
// screen entirely.
//
// Centring in the free space rather than in the panel is what keeps the mark on screen at
// all: a list whose last row happens to reach the middle would otherwise hide it.
func clearBand(c *lipgloss.Canvas, area kit.Rect, x, width int) (int, bool) {
	blank := func(y int) bool {
		for cx := x; cx < x+width; cx++ {
			if cell := c.CellAt(cx, y); cell != nil && cell.Content != " " && cell.Content != "" {
				return false
			}
		}
		return true
	}
	bestStart, bestLen := 0, -1
	start := -1
	closeRun := func(end int) {
		if l := end - start; l > bestLen {
			bestStart, bestLen = start, l
		}
	}
	for y := area.Y; y < area.Bottom(); y++ {
		switch b := blank(y); {
		case b && start < 0:
			start = y
		case !b && start >= 0:
			closeRun(y)
			start = -1
		}
	}
	if start >= 0 {
		closeRun(area.Bottom())
	}
	if bestLen < wordmarkRows {
		return 0, false
	}
	return bestStart + (bestLen-wordmarkRows)/2, true
}
