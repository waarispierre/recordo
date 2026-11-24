package tui

import (
	"fmt"
	"image/color"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	"github.com/waarispierre/recordo/internal/config"
)

// rect is an area of the screen in cells.
type rect struct{ x, y, w, h int }

func (r rect) inner() rect { return rect{r.x + 1, r.y + 1, max(r.w-2, 0), max(r.h-2, 0)} }
func (r rect) bottom() int { return r.y + r.h }
func (r rect) row(i int) rect {
	return rect{r.x, r.y + i, r.w, 1}
}

// put clears r and draws s into it, clipped to r — ratatui's Clear followed by a
// Paragraph.
func put(c *lipgloss.Canvas, r rect, s string) {
	if r.w <= 0 || r.h <= 0 {
		return
	}
	uv.NewStyledString(s).Draw(c, uv.Rect(r.x, r.y, r.w, r.h))
}

func rgbColour(r, g, b uint8) color.Color { return color.RGBA{r, g, b, 0xff} }

// layout splits the screen into the tab bar, the panel and the status bar.
func (m *Model) layout() (top, mid, bot rect) {
	// Inset, so the backdrop reads as a frame around the app rather than as noise behind
	// it.
	body := rect{2, 1, max(m.width-4, 0), max(m.height-2, 0)}
	topH := min(3, body.h)
	botH := min(3, body.h-topH)
	midH := body.h - topH - botH
	top = rect{body.x, body.y, body.w, topH}
	mid = rect{body.x, body.y + topH, body.w, midH}
	bot = rect{body.x, body.y + topH + midH, body.w, botH}
	return top, mid, bot
}

// bodyRows is the height of the panel between the tab and status bars.
func (m *Model) bodyRows() int {
	_, mid, _ := m.layout()
	return mid.h
}

func (m *Model) popupWidth(percent int) int { return m.width * percent / 100 }

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
	full := rect{0, 0, m.width, m.height}
	drawBackground(c, full)

	top, mid, bot := m.layout()
	// Cleared, so the backdrop does not show through a panel's empty interior.
	for _, r := range []rect{top, mid, bot} {
		put(c, r, "")
	}

	m.drawTabs(c, top)
	switch {
	case m.countdown != nil:
		m.drawCountdown(c, mid)
	case m.rec != nil:
		m.drawRecording(c, mid)
	case m.job != nil:
		m.drawJob(c, mid)
	case m.tab == tabSettings:
		m.drawSettings(c, mid)
	case m.tab == tabRecordings:
		m.drawRecordings(c, mid)
	default:
		m.drawRecord(c, mid)
	}
	m.drawStatus(c, bot)
	// Last, so it can see what the panel actually drew and stay out of its way.
	drawWordmark(c, mid)

	if m.editing != nil {
		m.drawEditPopup(c, full)
	}
	if m.confirm != nil {
		m.drawConfirmPopup(c, full)
	}
	if m.reporting {
		m.drawReportPopup(c, full)
	}
	return c.Render()
}

// drawBackground is a faint diagonal dot texture behind the whole app — drawn first, so
// every panel drawn afterwards simply overwrites the cells it occupies. No image support
// is needed (or added): terminals without a graphics protocol can't show one anyway.
func drawBackground(c *lipgloss.Canvas, r rect) {
	lines := make([]string, r.h)
	row := make([]rune, r.w)
	for y := range r.h {
		for x := range r.w {
			row[x] = ' '
			if (x+y*2)%6 == 0 {
				row[x] = '·'
			}
		}
		lines[y] = string(row)
	}
	put(c, r, plain.Foreground(rgbColour(42, 34, 58)).Render(strings.Join(lines, "\n")))
}

// drawBlock clears r, draws a bordered box with title on its top edge, and returns the
// area inside the border.
func drawBlock(c *lipgloss.Canvas, r rect, title string, border lipgloss.Style) rect {
	put(c, r, "")
	if r.w < 2 || r.h < 2 {
		return rect{}
	}
	title = ansi.Truncate(title, r.w-2, "")
	tw := ansi.StringWidth(title)
	put(c, r.row(0), border.Render("┌")+title+border.Render(strings.Repeat("─", r.w-2-tw)+"┐"))
	put(c, r.row(r.h-1), border.Render("└"+strings.Repeat("─", r.w-2)+"┘"))
	for y := 1; y < r.h-1; y++ {
		put(c, rect{r.x, r.y + y, 1, 1}, border.Render("│"))
		put(c, rect{r.x + r.w - 1, r.y + y, 1, 1}, border.Render("│"))
	}
	return r.inner()
}

// drawList draws items in a bordered list, the selected one highlighted behind a "❯ ".
// With nothing selected there is no marker column, as in ratatui.
func drawList(c *lipgloss.Canvas, r rect, title string, items []string, st listState) {
	inner := drawBlock(c, r, title, plain)
	for i := st.offset; i < len(items) && i-st.offset < inner.h; i++ {
		row := inner.row(i - st.offset)
		switch {
		case st.selected < 0:
			put(c, row, items[i])
		case i == st.selected:
			text := ansi.Truncate("❯ "+ansi.Strip(items[i]), row.w, "")
			text += strings.Repeat(" ", max(row.w-ansi.StringWidth(text), 0))
			put(c, row, highlight.Render(text))
		default:
			put(c, row, "  "+items[i])
		}
	}
}

// wrap word-wraps each line to width, as a ratatui Paragraph with Wrap does.
func wrap(lines []string, width int) string {
	if width <= 0 {
		return ""
	}
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		out = append(out, lipgloss.Wrap(l, width, ""))
	}
	return strings.Join(out, "\n")
}

func (m *Model) drawTabs(c *lipgloss.Canvas, r rect) {
	var b strings.Builder
	b.WriteString(plain.Foreground(magenta).Bold(true).Render(" recordo "))
	for _, t := range tabs {
		style := dim
		if t == m.tab {
			style = plain.Foreground(black).Background(cyan).Bold(true)
		}
		b.WriteString(style.Render(" " + t.title() + " "))
		b.WriteString(" ")
	}
	if rec := m.rec; rec != nil {
		// ~1s on/off — a pulse fast enough to read as "live" without being distracting.
		lit := time.Since(rec.started).Milliseconds()/500%2 == 0
		switch {
		case rec.paused:
			b.WriteString(plain.Foreground(yellow).Render("❚❚"))
		case lit:
			b.WriteString(plain.Foreground(red).Render("●"))
		default:
			b.WriteString(dim.Render("●"))
		}
	}
	put(c, r.row(0), b.String())
	if r.h > 1 {
		put(c, r.row(r.h-1), strings.Repeat("─", r.w))
	}
}

func (m *Model) drawRecord(c *lipgloss.Canvas, r rect) {
	labels := []string{"entire display"}
	for _, w := range m.windows {
		labels = append(labels, w.Label())
	}
	drawList(c, r, " what to record ", labels, m.windowList)
	m.drawPreview(c, r)
}

// drawPreview is the pre-flight framing check, in the corner the webcam settings put it.
func (m *Model) drawPreview(c *lipgloss.Canvas, area rect) {
	if !m.previewUp() {
		return
	}
	if m.thumb != nil {
		drawWebcamPip(c, area, m.previewSettings, *m.thumb)
		return
	}
	// The camera takes a moment to deliver its first frame; saying so beats a corner that
	// just sits empty.
	spot := rect{area.x + 2, max(area.bottom()-2, area.y), min(max(area.w-4, 0), 20), 1}
	put(c, spot, dim.Render("waking the camera…"))
}

// drawCountdown is the pre-roll: what is about to be recorded, how long is left, and the
// camera.
func (m *Model) drawCountdown(c *lipgloss.Canvas, r rect) {
	cd := m.countdown
	inner := drawBlock(c, r, " starting ", plain.Foreground(yellow))
	left := max(cd.length-time.Since(cd.started), 0)
	// Round up, so a full second is on screen for each number rather than flashing 0.
	seconds := int64(left / time.Second)
	if left%time.Second >= time.Millisecond {
		seconds++
	}
	put(c, inner, strings.Join([]string{
		dim.Render("● ") + bold.Render(fmt.Sprintf("recording in %d", seconds)),
		dim.Render("check your framing"),
		dim.Render("enter start now · esc cancel"),
	}, "\n"))
	m.drawPreview(c, inner)
}

func (m *Model) drawSettings(c *lipgloss.Canvas, r rect) {
	leftW := r.w * 62 / 100
	left := rect{r.x, r.y, leftW, r.h}
	right := rect{r.x + leftW, r.y, r.w - leftW, r.h}

	width := 0
	for _, s := range m.settings {
		width = max(width, len(s.Key))
	}
	items := make([]string, len(m.settings))
	for i, s := range m.settings {
		item := fmt.Sprintf("%-*s  ", width, s.Key)
		// A colour is far easier to judge as a block than as three floats.
		if r, g, b, ok := config.ParseRGB(s.Value); ok {
			item += plain.Foreground(rgbColour(r, g, b)).Render("██ ")
		}
		items[i] = item + dim.Render(s.Value)
	}
	drawList(c, left, " settings ", items, m.settingList)

	help := ""
	if s, ok := m.selectedSetting(); ok {
		help = s.Help
	}
	inner := drawBlock(c, right, " what it does ", plain)
	put(c, inner, plain.Foreground(gray).Render(wrap([]string{help}, inner.w)))
}

func (m *Model) drawRecordings(c *lipgloss.Canvas, r rect) {
	if len(m.recordings) == 0 {
		drawList(c, r, " recordings ", []string{"no recordings yet — press 1 to make one"}, listState{selected: -1})
		return
	}
	items := make([]string, len(m.recordings))
	for i, s := range m.recordings {
		state := plain.Foreground(yellow).Render("raw only")
		if s.HasExport() {
			state = plain.Foreground(green).Render("rendered")
		}
		items[i] = filepath.Base(s.Dir) + "  " + state
	}
	drawList(c, r, " recordings ", items, m.recordingList)
}

// drawRecording is the panel shown while a capture is in flight: elapsed time, what is
// being recorded, and — when the webcam is on — a live thumbnail in the configured
// corner.
func (m *Model) drawRecording(c *lipgloss.Canvas, r rect) {
	rec := m.rec
	inner := drawBlock(c, r, " recording ", plain)
	elapsed := rec.recorded()
	secs := int64(elapsed / time.Second)
	first := plain.Foreground(red).Render("● ")
	if rec.paused {
		first = plain.Foreground(yellow).Render("❚❚ ")
	}
	first += fmt.Sprintf("%02d:%02d", secs/60, secs%60)
	if rec.paused {
		first += plain.Foreground(yellow).Render("  paused")
	}
	lines := []string{first}
	if rec.label != "" {
		lines = append(lines, dim.Render(rec.label))
	}
	hint := "p/space pause · enter/s stop · q stop and quit"
	if rec.stopping {
		hint = "stopping — finishing the file"
	}
	lines = append(lines, dim.Render(hint))
	put(c, inner, strings.Join(lines, "\n"))

	// Drawn last so the overlay sits on top of the mark rather than under it.
	if rec.preview && m.thumb != nil {
		drawWebcamPip(c, inner, rec.settings, *m.thumb)
	}
}

// drawJob replaces the tab's own content with a status panel while a job is running —
// simpler than threading job state through each tab's drawing, and a job's progress
// matters regardless of which tab the user is looking at.
func (m *Model) drawJob(c *lipgloss.Canvas, r rect) {
	j := m.job
	inner := drawBlock(c, r, " working ", plain)
	var lines []string
	switch j.kind {
	case jobRender:
		// total 0 means the first frame hasn't landed yet, which is a distinct state from
		// 0/N — ffmpeg is still starting up and there is nothing to show.
		if j.total == 0 {
			lines = []string{"rendering", "starting ffmpeg…"}
		} else {
			lines = []string{"rendering", fmt.Sprintf("%s  %d/%d frames",
				m.bar.ViewAs(float64(j.done)/float64(j.total)), j.done, j.total)}
		}
	case jobCompress:
		lines = []string{"compressing", m.spin.View()}
	}
	put(c, inner, strings.Join(lines, "\n"))
}

func (m *Model) drawStatus(c *lipgloss.Canvas, r rect) {
	if r.h == 0 {
		return
	}
	put(c, r.row(0), strings.Repeat("─", r.w))
	if r.h > 1 {
		put(c, r.row(1), plain.Foreground(white).Render(" "+m.status+" ")+
			"  "+m.help.ShortHelpView(m.keys.statusKeys(m.tab)))
	}
}

func centered(percentX, percentY int, area rect) rect {
	w, h := area.w*percentX/100, area.h*percentY/100
	return rect{area.x + (area.w-w)/2, area.y + (area.h-h)/2, w, h}
}

func (m *Model) drawEditPopup(c *lipgloss.Canvas, full rect) {
	e := m.editing
	var title, hint string
	switch e.kind {
	case editSetting:
		title, hint = e.key, ""
		if config.IsColourKey(e.key) {
			hint = "hex like #5C66C7, or r, g, b"
		} else if s, ok := m.selectedSetting(); ok {
			hint = "now " + s.Value
		}
	case editCompressSize:
		title, hint = "compress "+filepath.Base(e.dir), "target size in MB, e.g. 25"
	case editLabel:
		title, hint = "name", "a label kept after the timestamp · blank removes it"
	}
	inner := drawBlock(c, centered(60, 30, full), " "+title+" ", plain.Foreground(cyan))
	put(c, inner, wrap([]string{dim.Render(hint), ""}, inner.w)+"\n"+e.input.View())
}

// drawConfirmPopup asks before anything irreversible, spelling out what goes and how
// much it frees.
func (m *Model) drawConfirmPopup(c *lipgloss.Canvas, full rect) {
	cf := m.confirm
	mb := float64(cf.bytes) / 1_000_000
	name := filepath.Base(cf.dir)
	var title string
	var lines []string
	switch cf.kind {
	case confirmDelete:
		title = " delete recording "
		lines = []string{name, "",
			fmt.Sprintf("moves the whole folder to the Trash, %.1f MB", mb),
			"the rendered video goes with it"}
	case confirmDropRaw:
		title = " delete raw files "
		lines = []string{name, "",
			fmt.Sprintf("deletes the capture and sidecars, freeing %.1f MB", mb),
			"the rendered videos stay · this one is not undoable"}
	}
	lines = append(lines, "", plain.Foreground(red).Render("y")+dim.Render(" yes · any other key cancels"))
	inner := drawBlock(c, centered(56, 34, full), title, plain.Foreground(red))
	put(c, inner, wrap(lines, inner.w))
}

func (m *Model) drawReportPopup(c *lipgloss.Canvas, full rect) {
	inner := drawBlock(c, centered(64, 50, full), " done — press any key ", plain.Foreground(green))
	put(c, inner, wrap(strings.Split(m.report, "\n"), inner.w))
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
func drawWordmark(c *lipgloss.Canvas, area rect) {
	var pixelRows [6][]rune
	for r := range 6 {
		parts := make([]string, len(wordmarkGlyphs))
		for i, g := range wordmarkGlyphs {
			parts[i] = g[r]
		}
		pixelRows[r] = []rune(strings.Join(parts, "  "))
	}
	width := max(len(pixelRows[0]), len(tagline))
	if area.w < width || area.h < wordmarkRows {
		return
	}
	x := area.x + (area.w-width)/2
	y, ok := clearBand(c, area, x, width)
	if !ok {
		return
	}

	centre := func(s string) string {
		return strings.Repeat(" ", (width-ansi.StringWidth(s))/2) + s
	}
	mark := plain.Foreground(rgbColour(96, 68, 132))
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
	lines = append(lines, "", centre(plain.Foreground(rgbColour(48, 41, 62)).Render(tagline)))
	put(c, rect{x, y, width, wordmarkRows}, strings.Join(lines, "\n"))
}

// clearBand is the top row at which wordmarkRows rows fit in the tallest run of rows the
// panel left untouched across the mark's columns, centred within that run. ok is false
// when no run is tall enough, which is how a full settings list keeps the mark off the
// screen entirely.
//
// Centring in the free space rather than in the panel is what keeps the mark on screen at
// all: a list whose last row happens to reach the middle would otherwise hide it.
func clearBand(c *lipgloss.Canvas, area rect, x, width int) (int, bool) {
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
	for y := area.y; y < area.bottom(); y++ {
		switch b := blank(y); {
		case b && start < 0:
			start = y
		case !b && start >= 0:
			closeRun(y)
			start = -1
		}
	}
	if start >= 0 {
		closeRun(area.bottom())
	}
	if bestLen < wordmarkRows {
		return 0, false
	}
	return bestStart + (bestLen-wordmarkRows)/2, true
}
