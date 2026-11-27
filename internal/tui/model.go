package tui

import (
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/progress"
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/waarispierre/recordo/internal/features/record"
	"github.com/waarispierre/recordo/internal/features/recordings"
	"github.com/waarispierre/recordo/internal/features/settings"
	"github.com/waarispierre/recordo/internal/tui/kit"
)

// Keys only the shell handles.
var shellKeys = struct {
	prev key.Binding
}{
	prev: kit.Bind("shift+tab", "switch back", "shift+tab", "h"),
}

// job is a render or compress running on a goroutine for one of the tabs.
type job struct {
	kind    kit.JobKind
	started time.Time
	// progress is written by the render's callback once per frame and read on the tick:
	// sending a message per frame would redraw the screen hundreds of times a second.
	progress *jobProgress
	// done and total are the last progress seen; total 0 means no frame has landed yet.
	done, total int
}

// jobProgress is (frames done, frames total), packed into one word so a reader never sees
// one half of an update.
type jobProgress struct{ v atomic.Uint64 }

func (p *jobProgress) set(done, total int) {
	p.v.Store(uint64(uint32(done))<<32 | uint64(uint32(total)))
}

func (p *jobProgress) get() (done, total int) {
	v := p.v.Load()
	return int(v >> 32), int(uint32(v))
}

type (
	tickMsg    struct{}
	jobDoneMsg struct {
		text string
		err  error
	}
)

// Model is the shell. It is always used by pointer, and is the kit.Host every tab talks
// back through.
type Model struct {
	width, height int

	tabs   []kit.Tab
	active int

	job *job
	// report is a finished job's report, shown as a popup until the next keypress.
	report    string
	reporting bool
	status    string
	// quitting is set when the user quit during a job: the app ends once it finishes, so
	// quitting never leaves ffmpeg writing a half-finished file behind the app's back.
	quitting bool
	ticking  bool
	// fatal ends the app with an error, as the Rust event loop's `?` did.
	fatal error
	// farewell is printed once the terminal is back, if quitting left something the user
	// would otherwise never see.
	farewell string

	help help.Model
	bar  progress.Model
	spin spinner.Model

	// bg tracks every goroutine the app started, so Run can wait for a recording or a
	// render to finish before the process exits.
	bg sync.WaitGroup
	// observe, when set, runs at the end of every Update. Tests use it to look at the
	// model on the program's own goroutine.
	observe func(*Model)
}

// New builds the app with the given side effects; Run uses the real ones.
func New(o Overrides, configPath string, d Deps) (*Model, error) {
	m := &Model{status: kit.DefaultStatus, help: newHelp(), bar: newBar(), spin: newSpinner()}
	st, err := settings.NewTab(m, configPath)
	if err != nil {
		return nil, err
	}
	m.tabs = []kit.Tab{
		record.NewTab(m, o, configPath, d.Record),
		st,
		recordings.NewTab(m, o, d.Recordings),
	}
	return m, nil
}

func (m *Model) Init() tea.Cmd { return nil }

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	cmd := m.update(msg)
	if m.observe != nil {
		m.observe(m)
	}
	return m, tea.Batch(cmd, m.ensureTick())
}

func (m *Model) update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		_, mid, _ := m.layout()
		return m.broadcast(kit.LayoutMsg{Width: msg.Width, Height: msg.Height, PanelRows: mid.H})
	case tickMsg:
		m.ticking = false
		if j := m.job; j != nil {
			j.done, j.total = j.progress.get()
		}
		return m.broadcast(kit.TickMsg{})
	case spinner.TickMsg:
		if m.job != nil && m.job.kind == kit.JobCompress {
			var cmd tea.Cmd
			m.spin, cmd = m.spin.Update(msg)
			return cmd
		}
		return nil
	case jobDoneMsg:
		return m.onJobDone(msg)
	case tea.KeyPressMsg:
		return m.onKey(msg)
	}
	return m.broadcast(msg)
}

func (m *Model) broadcast(msg tea.Msg) tea.Cmd {
	cmds := make([]tea.Cmd, len(m.tabs))
	for i, t := range m.tabs {
		cmds[i] = t.Update(msg)
	}
	return tea.Batch(cmds...)
}

// ensureTick keeps a 100ms tick running while anything on screen moves on its own.
func (m *Model) ensureTick() tea.Cmd {
	animating := m.job != nil
	for _, t := range m.tabs {
		animating = animating || t.Animating()
	}
	if m.ticking || !animating {
		return nil
	}
	m.ticking = true
	return tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg { return tickMsg{} })
}

func (m *Model) capturing() kit.Tab {
	for _, t := range m.tabs {
		if t.Capturing() {
			return t
		}
	}
	return nil
}

func (m *Model) onKey(k tea.KeyPressMsg) tea.Cmd {
	if t := m.capturing(); t != nil {
		return t.Update(k)
	}
	if m.reporting {
		m.reporting = false
		m.report = ""
		m.status = kit.DefaultStatus
		return nil
	}
	if m.job != nil {
		// Quitting waits for the job, so it never leaves ffmpeg writing into a
		// half-finished file.
		if key.Matches(k, kit.Quit) && !m.quitting {
			m.quitting = true
			m.status = "finishing up…"
		}
		return nil
	}
	active := m.tabs[m.active]
	if active.Modal() {
		return active.Update(k)
	}
	switch {
	case key.Matches(k, kit.Quit):
		return tea.Quit
	case key.Matches(k, kit.Switch):
		m.active = (m.active + 1) % len(m.tabs)
	case key.Matches(k, shellKeys.prev):
		m.active = (m.active + len(m.tabs) - 1) % len(m.tabs)
	case len(k.Text) == 1 && k.Text[0] >= '1' && int(k.Text[0]-'1') < len(m.tabs):
		m.active = int(k.Text[0] - '1')
	default:
		return active.Update(k)
	}
	return nil
}

func (m *Model) onJobDone(msg jobDoneMsg) tea.Cmd {
	m.job = nil
	if m.quitting {
		return tea.Quit
	}
	if msg.err != nil {
		m.ShowReport("failed: " + msg.err.Error())
	} else {
		m.ShowReport(msg.text)
	}
	m.Changed()
	return nil
}

// shutdown finalises anything still running once the program has ended.
func (m *Model) shutdown() {
	for _, t := range m.tabs {
		t.Stop()
	}
	m.bg.Wait()
	for _, t := range m.tabs {
		t.Close()
	}
}

// --- kit.Host -----------------------------------------------------------------------

var _ kit.Host = (*Model)(nil)

func (m *Model) SetStatus(s string) { m.status = s }

func (m *Model) ShowReport(lines ...string) {
	m.report = strings.Join(lines, "\n")
	m.reporting = true
	m.status = "press any key to dismiss"
}

func (m *Model) StartJob(kind kit.JobKind, status string, run func(progress func(done, total int)) (string, error)) tea.Cmd {
	p := &jobProgress{}
	m.job = &job{kind: kind, started: time.Now(), progress: p}
	m.status = status
	cmd := m.Background(func() tea.Msg {
		text, err := run(p.set)
		return jobDoneMsg{text: text, err: err}
	})
	if kind == kit.JobCompress {
		return tea.Batch(cmd, m.spin.Tick)
	}
	return cmd
}

// Background runs fn on a goroutine Run waits for, delivering its result as a message.
func (m *Model) Background(fn func() tea.Msg) tea.Cmd {
	ch := make(chan tea.Msg, 1)
	m.bg.Add(1)
	go func() {
		defer m.bg.Done()
		ch <- fn()
	}()
	return func() tea.Msg { return <-ch }
}

func (m *Model) Changed() {
	for _, t := range m.tabs {
		t.Refresh()
	}
}

func (m *Model) Farewell(text string) { m.farewell = text }

func (m *Model) Fail(err error) tea.Cmd {
	m.fatal = err
	return tea.Quit
}

// --- for tests ----------------------------------------------------------------------

// Observe runs fn on the program's goroutine at the end of every Update. For tests.
func (m *Model) Observe(fn func(*Model)) { m.observe = fn }

// Screen is what the app would draw now, without styling.
func (m *Model) Screen() string { return ansi.Strip(m.render()) }

// Status is the status bar's message.
func (m *Model) Status() string { return m.status }

// Report is the report popup's text, if one is up.
func (m *Model) Report() (string, bool) { return m.report, m.reporting }

// FarewellText is what Run will print once the terminal is back.
func (m *Model) FarewellText() string { return m.farewell }

// --- bubbles ------------------------------------------------------------------------

func newHelp() help.Model {
	h := help.New()
	h.ShortSeparator = " · "
	h.Styles.ShortKey = kit.Dim
	h.Styles.ShortDesc = kit.Dim
	h.Styles.ShortSeparator = kit.Dim
	return h
}

// barWidth is the width in cells of both job indicators, so a compress reads as the same
// kind of thing as a render.
const barWidth = 24

// newBar is the render progress bar: barWidth cells of ▰/▱, with the frame count beside
// it rather than a percentage.
func newBar() progress.Model {
	bar := progress.New(
		progress.WithFillCharacters('▰', '▱'),
		progress.WithWidth(barWidth),
		progress.WithoutPercentage(),
	)
	bar.FullColor, bar.EmptyColor = lipgloss.NoColor{}, lipgloss.NoColor{}
	return bar
}

// newSpinner is the compress indicator. ffmpeg reports no progress for it, so rather than
// a fill it is a short segment sweeping back and forth across a bar as wide as the render
// bar.
func newSpinner() spinner.Model {
	return spinner.New(spinner.WithSpinner(spinner.Spinner{
		Frames: sweepFrames(barWidth, 6),
		FPS:    60 * time.Millisecond,
	}))
}

// sweepFrames are the frames of a seg-cell ▰ segment travelling across a width-cell ▱ bar
// and back, without repeating a frame at either end.
func sweepFrames(width, seg int) []string {
	frame := func(at int) string {
		return strings.Repeat("▱", at) + strings.Repeat("▰", seg) + strings.Repeat("▱", width-seg-at)
	}
	var frames []string
	for at := 0; at <= width-seg; at++ {
		frames = append(frames, frame(at))
	}
	for at := width - seg - 1; at > 0; at-- {
		frames = append(frames, frame(at))
	}
	return frames
}
