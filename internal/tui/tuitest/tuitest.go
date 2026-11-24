// Package tuitest drives the whole app in tests: the real shell and tabs, with every side
// effect replaced by a fake. The sandbox the tests run in has no screen recording, camera
// or GPU, so this is how the tabs' flows are tested end to end. Test code only.
package tuitest

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/exp/teatest/v2"

	"github.com/waarispierre/recordo/internal/domain/config"
	"github.com/waarispierre/recordo/internal/domain/pauses"
	"github.com/waarispierre/recordo/internal/domain/session"
	"github.com/waarispierre/recordo/internal/features/compress"
	"github.com/waarispierre/recordo/internal/features/record"
	"github.com/waarispierre/recordo/internal/features/recordings"
	"github.com/waarispierre/recordo/internal/features/render"
	"github.com/waarispierre/recordo/internal/platform/macos"
	"github.com/waarispierre/recordo/internal/tui"
)

// Grey is a flat mid-grey frame.
func Grey(w, h uint32) macos.PreviewFrame {
	return macos.PreviewFrame{RGB: bytes.Repeat([]byte{128}, int(w*h*3)), W: w, H: h}
}

// FakeCam is a framing camera serving a grey frame until stopped.
type FakeCam struct {
	mu      sync.Mutex
	stopped bool
}

func (c *FakeCam) Preview() (macos.PreviewFrame, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stopped {
		return macos.PreviewFrame{}, false
	}
	return Grey(32, 24), true
}
func (c *FakeCam) Name() string { return "Fake Cam" }
func (c *FakeCam) Stop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stopped = true
}

// Stopped reports whether the camera was released.
func (c *FakeCam) Stopped() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stopped
}

// Fakes records what the app asked of the outside world. Set the exported knobs before
// the app starts; read what happened through Snapshot.
type Fakes struct {
	mu sync.Mutex

	// RecordErr fails every recording.
	RecordErr error
	// RenderGate, when set, holds a render at frame 50/100 until closed.
	RenderGate chan struct{}
	// CompressGate, when set, holds a compress until closed.
	CompressGate chan struct{}

	got Calls
}

// Calls is what the app did.
type Calls struct {
	Targets  []record.Target
	Configs  []config.Config
	Sessions []string
	Pauses   []pauses.Pause
	// CamsReleased is, per recording, whether every framing camera had been released
	// when it began.
	CamsReleased []bool
	Renders      []render.Options
	Cams         []*FakeCam
	Trashed      []string
	Opened       []string
}

// Snapshot is a copy of what the app has done so far.
func (f *Fakes) Snapshot() Calls {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.got
	c.Targets = append([]record.Target(nil), c.Targets...)
	c.Configs = append([]config.Config(nil), c.Configs...)
	c.Sessions = append([]string(nil), c.Sessions...)
	c.Pauses = append([]pauses.Pause(nil), c.Pauses...)
	c.CamsReleased = append([]bool(nil), c.CamsReleased...)
	c.Renders = append([]render.Options(nil), c.Renders...)
	c.Cams = append([]*FakeCam(nil), c.Cams...)
	c.Trashed = append([]string(nil), c.Trashed...)
	c.Opened = append([]string(nil), c.Opened...)
	return c
}

// Deps are the app's side effects, faked. One window is on screen: Safari, id 42.
func (f *Fakes) Deps() tui.Deps {
	renderFn := func(src, dst string, o render.Options) (render.Report, error) {
		f.mu.Lock()
		f.got.Renders = append(f.got.Renders, o)
		gate := f.RenderGate
		f.mu.Unlock()
		o.Progress(50, 100)
		if gate != nil {
			<-gate
		}
		o.Progress(100, 100)
		if err := os.WriteFile(dst, []byte("export"), 0o600); err != nil {
			return render.Report{}, err
		}
		return render.Report{Frames: 100, VideoSeconds: 2, RenderSeconds: 1, OutW: 1920, OutH: 1080}, nil
	}
	open := func(path string) {
		f.mu.Lock()
		f.got.Opened = append(f.got.Opened, path)
		f.mu.Unlock()
	}
	return tui.Deps{
		Record: record.TabDeps{
			Windows: func() []macos.Window {
				return []macos.Window{{ID: 42, AppName: "Safari", Title: "Docs", Frame: macos.Rect{W: 1200, H: 800}}}
			},
			Record: func(s *session.Session, t record.Target, cfg config.Config, o record.RecordOptions) error {
				f.mu.Lock()
				f.got.Targets = append(f.got.Targets, t)
				f.got.Configs = append(f.got.Configs, cfg)
				f.got.Sessions = append(f.got.Sessions, s.Dir)
				released := true
				for _, c := range f.got.Cams {
					released = released && c.Stopped()
				}
				f.got.CamsReleased = append(f.got.CamsReleased, released)
				err := f.RecordErr
				f.mu.Unlock()
				if err != nil {
					return err
				}
				o.OnStart(&record.Plan{Label: "Safari — Docs (1200x800)", Microphone: "Fake Mic"})
				log := pauses.NewLog(func() uint64 { return uint64(time.Now().UnixNano()) })
				o.Stop(log)
				o.Warn("a test warning")
				f.mu.Lock()
				f.got.Pauses = log.Finish(uint64(time.Now().UnixNano()))
				f.mu.Unlock()
				return os.WriteFile(s.Capture(), []byte("capture"), 0o600)
			},
			Health: func(*session.Session) (record.Health, error) {
				return record.Health{Frames: 10, CursorSamples: 20, Clicks: 2, MedianGapMs: 5, TapInstalled: true}, nil
			},
			Render: renderFn,
			OpenCamera: func(string) (record.FramingCamera, error) {
				c := &FakeCam{}
				f.mu.Lock()
				f.got.Cams = append(f.got.Cams, c)
				f.mu.Unlock()
				return c, nil
			},
			Open: open,
		},
		Recordings: recordings.TabDeps{
			Render: renderFn,
			Compress: func(src, dst string, maxBytes uint64) (compress.CompressReport, error) {
				f.mu.Lock()
				gate := f.CompressGate
				f.mu.Unlock()
				if gate != nil {
					<-gate
				}
				return compress.CompressReport{InputBytes: 6, OutputBytes: 6, MaxBytes: maxBytes}, nil
			},
			Trash: func(dir string) error {
				f.mu.Lock()
				f.got.Trashed = append(f.got.Trashed, dir)
				f.mu.Unlock()
				return os.RemoveAll(dir)
			},
			Open: open,
		},
	}
}

type waiter struct {
	pred func(*tui.Model) bool
	done chan struct{}
}

// Harness is the app running in a teatest program, in a home directory of its own.
type Harness struct {
	T          *testing.T
	TM         *teatest.TestModel
	Fakes      *Fakes
	ConfigPath string
	Recordings string

	mu      sync.Mutex
	waiters []*waiter
}

type pokeMsg struct{}

// Home gives the test a fresh home directory holding a default config, so the config and
// recordings the app reads and writes are the test's own. It returns the config path and
// the recordings directory.
func Home(t *testing.T) (configPath, recordings string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	configPath, err := session.ConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := config.LoadOrCreate(configPath); err != nil {
		t.Fatal(err)
	}
	recordings, err = session.RecordingsDir()
	if err != nil {
		t.Fatal(err)
	}
	return configPath, recordings
}

// Start runs the app at 120x40. setup runs first, to write recordings or settings.
func Start(t *testing.T, o tui.Overrides, setup func(h *Harness)) *Harness {
	t.Helper()
	configPath, recordings := Home(t)
	h := &Harness{T: t, Fakes: &Fakes{}, ConfigPath: configPath, Recordings: recordings}
	if setup != nil {
		setup(h)
	}
	m, err := tui.New(o, configPath, h.Fakes.Deps())
	if err != nil {
		t.Fatal(err)
	}
	m.Observe(h.observe)
	h.TM = teatest.NewTestModel(t, m, teatest.WithInitialTermSize(120, 40))
	h.WaitScreen("recordo")
	return h
}

// observe runs on the program's goroutine after every update.
func (h *Harness) observe(m *tui.Model) {
	h.mu.Lock()
	defer h.mu.Unlock()
	kept := h.waiters[:0]
	for _, w := range h.waiters {
		if w.pred(m) {
			close(w.done)
		} else {
			kept = append(kept, w)
		}
	}
	h.waiters = kept
}

// WaitFor blocks until pred holds for the app, evaluated on the program's goroutine.
func (h *Harness) WaitFor(what string, pred func(*tui.Model) bool) {
	h.T.Helper()
	w := &waiter{pred: pred, done: make(chan struct{})}
	h.mu.Lock()
	h.waiters = append(h.waiters, w)
	h.mu.Unlock()
	h.TM.Send(pokeMsg{})
	select {
	case <-w.done:
	case <-time.After(5 * time.Second):
		var screen, status string
		h.Inspect(func(m *tui.Model) { screen, status = m.Screen(), m.Status() })
		h.T.Fatalf("timed out waiting for %s; status %q, screen:\n%s", what, status, screen)
	}
}

// Inspect runs fn against the app on the program's goroutine.
func (h *Harness) Inspect(fn func(*tui.Model)) {
	done := make(chan struct{})
	h.mu.Lock()
	h.waiters = append(h.waiters, &waiter{pred: func(m *tui.Model) bool { fn(m); return true }, done: done})
	h.mu.Unlock()
	h.TM.Send(pokeMsg{})
	select {
	case <-done:
	case <-time.After(5 * time.Second):
	}
}

// WaitScreen waits for text to be on screen.
func (h *Harness) WaitScreen(text string) {
	h.T.Helper()
	h.WaitFor("screen showing "+text, func(m *tui.Model) bool { return strings.Contains(m.Screen(), text) })
}

// WaitGone waits for text to leave the screen.
func (h *Harness) WaitGone(text string) {
	h.T.Helper()
	h.WaitFor("screen without "+text, func(m *tui.Model) bool { return !strings.Contains(m.Screen(), text) })
}

// WaitStatus waits for the status bar to say want.
func (h *Harness) WaitStatus(want string) {
	h.T.Helper()
	h.WaitFor("status "+want, func(m *tui.Model) bool { return m.Status() == want })
}

// WaitStatusPrefix waits for the status bar to start with prefix, and returns it.
func (h *Harness) WaitStatusPrefix(prefix string) string {
	h.T.Helper()
	var got string
	h.WaitFor("status starting "+prefix, func(m *tui.Model) bool {
		got = m.Status()
		return strings.HasPrefix(got, prefix)
	})
	return got
}

// WaitReport waits for the report popup and returns its text.
func (h *Harness) WaitReport() string {
	h.T.Helper()
	var report string
	h.WaitFor("the report", func(m *tui.Model) bool {
		var ok bool
		report, ok = m.Report()
		return ok
	})
	return report
}

// Press sends keys.
func (h *Harness) Press(keys ...tea.KeyPressMsg) {
	for _, k := range keys {
		h.TM.Send(k)
	}
}

// Type types text.
func (h *Harness) Type(s string) { h.TM.Type(s) }

// Quit presses q and waits for the app to end.
func (h *Harness) Quit() *tui.Model {
	h.T.Helper()
	h.Press(Char('q'))
	return h.Finish()
}

// Finish waits for the app to end.
func (h *Harness) Finish() *tui.Model {
	h.T.Helper()
	return h.TM.FinalModel(h.T, teatest.WithFinalTimeout(5*time.Second)).(*tui.Model)
}

// MakeRecording creates a recording folder holding the named files, ten bytes each.
func (h *Harness) MakeRecording(name string, files ...string) string {
	dir := filepath.Join(h.Recordings, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		h.T.Fatal(err)
	}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("0123456789"), 0o600); err != nil {
			h.T.Fatal(err)
		}
	}
	return dir
}

// Char is a printable key.
func Char(r rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: r, Text: string(r)} }

var (
	Enter   = tea.KeyPressMsg{Code: tea.KeyEnter}
	Esc     = tea.KeyPressMsg{Code: tea.KeyEscape}
	Tab     = tea.KeyPressMsg{Code: tea.KeyTab}
	BackTab = tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
	Space   = tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
)

// Exists reports whether path exists.
func Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
