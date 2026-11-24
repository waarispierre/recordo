package tui

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/teatest/v2"

	"github.com/waarispierre/recordo/internal/capture"
	"github.com/waarispierre/recordo/internal/config"
	"github.com/waarispierre/recordo/internal/pauses"
	"github.com/waarispierre/recordo/internal/render"
	"github.com/waarispierre/recordo/internal/session"
)

// --- fakes --------------------------------------------------------------------------

type fakeCam struct {
	mu      sync.Mutex
	stopped bool
	frame   capture.PreviewFrame
}

func (c *fakeCam) Preview() (capture.PreviewFrame, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stopped {
		return capture.PreviewFrame{}, false
	}
	return c.frame, true
}
func (c *fakeCam) Name() string { return "Fake Cam" }
func (c *fakeCam) Stop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stopped = true
}
func (c *fakeCam) isStopped() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stopped
}

// fakes records what the app asked of the outside world. Fields are only read by the
// test after the app has signalled, through the model, that the call happened.
type fakes struct {
	mu sync.Mutex

	targets     []capture.Target
	configs     []config.Config
	pauses      []pauses.Pause
	recordErr   error
	camsAtStart []bool // whether every framing camera was released when recording began

	renderGate   chan struct{} // when set, the render blocks at frame 50 until closed
	compressGate chan struct{}
	renders      []render.Options

	cams    []*fakeCam
	trashed []string
	opened  []string
}

func (f *fakes) deps() deps {
	return deps{
		windows: func() []capture.Window {
			return []capture.Window{{ID: 42, AppName: "Safari", Title: "Docs", Frame: capture.Rect{W: 1200, H: 800}}}
		},
		record: func(s *session.Session, t capture.Target, cfg config.Config, o capture.RecordOptions) error {
			f.mu.Lock()
			f.targets = append(f.targets, t)
			f.configs = append(f.configs, cfg)
			released := true
			for _, c := range f.cams {
				released = released && c.isStopped()
			}
			f.camsAtStart = append(f.camsAtStart, released)
			err := f.recordErr
			f.mu.Unlock()
			if err != nil {
				return err
			}
			o.OnStart(&capture.Plan{Label: "Safari — Docs (1200x800)", Microphone: "Fake Mic"})
			log := pauses.NewLog(func() uint64 { return uint64(time.Now().UnixNano()) })
			o.Stop(log)
			o.Warn("a test warning")
			f.mu.Lock()
			f.pauses = log.Finish(uint64(time.Now().UnixNano()))
			f.mu.Unlock()
			return os.WriteFile(s.Capture(), []byte("capture"), 0o600)
		},
		health: func(*session.Session) (capture.Health, error) {
			return capture.Health{Frames: 10, CursorSamples: 20, Clicks: 2, MedianGapMs: 5, TapInstalled: true}, nil
		},
		render: func(src, dst string, o render.Options) (render.Report, error) {
			f.mu.Lock()
			f.renders = append(f.renders, o)
			gate := f.renderGate
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
		},
		compress: func(src, dst string, maxBytes uint64) (render.CompressReport, error) {
			f.mu.Lock()
			gate := f.compressGate
			f.mu.Unlock()
			if gate != nil {
				<-gate
			}
			return render.CompressReport{InputBytes: 6, OutputBytes: 6, MaxBytes: maxBytes}, nil
		},
		openCamera: func(string) (framingCamera, error) {
			c := &fakeCam{frame: grey(32, 24)}
			f.mu.Lock()
			f.cams = append(f.cams, c)
			f.mu.Unlock()
			return c, nil
		},
		trash: func(dir string) error {
			f.mu.Lock()
			f.trashed = append(f.trashed, dir)
			f.mu.Unlock()
			return os.RemoveAll(dir)
		},
		open: func(path string) {
			f.mu.Lock()
			f.opened = append(f.opened, path)
			f.mu.Unlock()
		},
	}
}

func (f *fakes) snapshot() fakes {
	f.mu.Lock()
	defer f.mu.Unlock()
	return fakes{
		targets: append([]capture.Target(nil), f.targets...), configs: append([]config.Config(nil), f.configs...),
		pauses: append([]pauses.Pause(nil), f.pauses...), camsAtStart: append([]bool(nil), f.camsAtStart...),
		renders: append([]render.Options(nil), f.renders...), cams: append([]*fakeCam(nil), f.cams...),
		trashed: append([]string(nil), f.trashed...), opened: append([]string(nil), f.opened...),
	}
}

// --- harness ------------------------------------------------------------------------

type waiter struct {
	pred func(*Model) bool
	done chan struct{}
}

type harness struct {
	t          *testing.T
	tm         *teatest.TestModel
	fakes      *fakes
	configPath string
	recordings string

	mu      sync.Mutex
	waiters []*waiter
}

type pokeMsg struct{}

// newHarness gives the app a fresh home directory, so the config and recordings it
// reads and writes are the test's own.
func newHarness(t *testing.T, o Overrides, setup func(h *harness)) *harness {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	configPath, err := session.ConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := config.LoadOrCreate(configPath); err != nil {
		t.Fatal(err)
	}
	recordings, err := session.RecordingsDir()
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, fakes: &fakes{}, configPath: configPath, recordings: recordings}
	if setup != nil {
		setup(h)
	}
	m, err := newModel(o, configPath, h.fakes.deps())
	if err != nil {
		t.Fatal(err)
	}
	m.afterUpdate = h.observe
	h.tm = teatest.NewTestModel(t, m, teatest.WithInitialTermSize(120, 40))
	h.waitFor("first frame", func(m *Model) bool { return m.width == 120 })
	return h
}

// observe runs on the program's goroutine after every update.
func (h *harness) observe(m *Model) {
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

// waitFor blocks until pred holds for the model, evaluated on the program's goroutine.
func (h *harness) waitFor(what string, pred func(*Model) bool) {
	h.t.Helper()
	w := &waiter{pred: pred, done: make(chan struct{})}
	h.mu.Lock()
	h.waiters = append(h.waiters, w)
	h.mu.Unlock()
	h.tm.Send(pokeMsg{})
	select {
	case <-w.done:
	case <-time.After(5 * time.Second):
		var screen, status string
		h.inspect(func(m *Model) { screen, status = ansi.Strip(m.render()), m.status })
		h.t.Fatalf("timed out waiting for %s; status %q, screen:\n%s", what, status, screen)
	}
}

// inspect runs fn against the model on the program's goroutine.
func (h *harness) inspect(fn func(*Model)) {
	done := make(chan struct{})
	h.mu.Lock()
	h.waiters = append(h.waiters, &waiter{pred: func(m *Model) bool { fn(m); return true }, done: done})
	h.mu.Unlock()
	h.tm.Send(pokeMsg{})
	select {
	case <-done:
	case <-time.After(5 * time.Second):
	}
}

func (h *harness) waitScreen(text string) {
	h.t.Helper()
	h.waitFor("screen showing "+text, func(m *Model) bool {
		return strings.Contains(ansi.Strip(m.render()), text)
	})
}

func (h *harness) waitStatus(want string) {
	h.t.Helper()
	h.waitFor("status "+want, func(m *Model) bool { return m.status == want })
}

func (h *harness) press(keys ...tea.KeyPressMsg) {
	for _, k := range keys {
		h.tm.Send(k)
	}
}

func (h *harness) typeText(s string) { h.tm.Type(s) }

func (h *harness) quit() *Model {
	h.t.Helper()
	h.press(char('q'))
	return h.finish()
}

func (h *harness) finish() *Model {
	h.t.Helper()
	return h.tm.FinalModel(h.t, teatest.WithFinalTimeout(5*time.Second)).(*Model)
}

func char(r rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: r, Text: string(r)} }

var (
	enter    = tea.KeyPressMsg{Code: tea.KeyEnter}
	esc      = tea.KeyPressMsg{Code: tea.KeyEscape}
	tabKey   = tea.KeyPressMsg{Code: tea.KeyTab}
	backTab  = tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
	spaceKey = tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
)

// makeRecording creates a recording folder holding the named files.
func (h *harness) makeRecording(name string, files ...string) string {
	dir := filepath.Join(h.recordings, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		h.t.Fatal(err)
	}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("0123456789"), 0o600); err != nil {
			h.t.Fatal(err)
		}
	}
	return dir
}

func (h *harness) selectSetting(key string) {
	h.t.Helper()
	settings, err := config.Settings(h.configPath)
	if err != nil {
		h.t.Fatal(err)
	}
	for i, s := range settings {
		if s.Key == key {
			h.inspect(func(m *Model) { m.settingList.selected = i })
			return
		}
	}
	h.t.Fatalf("no setting %s", key)
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// --- flows --------------------------------------------------------------------------

func TestTabSwitching(t *testing.T) {
	h := newHarness(t, Overrides{}, nil)
	h.waitScreen("what to record")
	h.waitScreen("Safari — Docs (1200x800)")
	// The wordmark's tagline, in the empty space under the short window list.
	h.waitScreen("screen recordings with a cursor-following camera")

	tabIs := func(want tab) func(*Model) bool { return func(m *Model) bool { return m.tab == want } }
	steps := []struct {
		key  tea.KeyPressMsg
		want tab
		text string
	}{
		{tabKey, tabSettings, "what it does"},
		{char('l'), tabRecordings, "no recordings yet — press 1 to make one"},
		{tabKey, tabRecord, "what to record"},
		{backTab, tabRecordings, " recordings "},
		{char('h'), tabSettings, "what it does"},
		{char('3'), tabRecordings, "enter open · r render"},
		{char('1'), tabRecord, "enter record · w preview · tab switch · q quit"},
		{char('2'), tabSettings, "enter edit · tab switch · q quit"},
	}
	for _, s := range steps {
		h.press(s.key)
		h.waitFor(s.want.title()+" tab", tabIs(s.want))
		h.waitScreen(s.text)
	}

	// j/k wrap around the list.
	h.press(char('1'), char('k'))
	h.waitFor("selection wrapped to the last window", func(m *Model) bool { return m.windowList.selected == 1 })
	h.press(char('j'))
	h.waitFor("selection wrapped to entire display", func(m *Model) bool { return m.windowList.selected == 0 })

	h.quit()
}

func TestSettingsEditValidatesAndRollsBack(t *testing.T) {
	h := newHarness(t, Overrides{}, nil)
	read := func() string {
		b, err := os.ReadFile(h.configPath)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	original := read()

	h.press(char('2'))
	h.selectSetting("camera.zoom_percent")
	h.press(enter)
	h.waitScreen("now ")
	h.typeText("lots")
	h.press(enter)
	h.waitFor("a rejection", func(m *Model) bool { return strings.HasPrefix(m.status, "rejected: ") })
	var status string
	h.inspect(func(m *Model) { status = m.status })
	if !strings.Contains(status, "— keeping ") {
		t.Errorf("status %q does not say what was kept", status)
	}
	if read() != original {
		t.Fatal("an invalid value was left in the config")
	}

	// Escape and blank both leave it alone.
	h.press(enter, esc)
	h.waitStatus("cancelled")
	h.press(enter, enter)
	h.waitStatus("unchanged")

	h.press(enter)
	h.typeText("30")
	h.press(enter)
	h.waitStatus("camera.zoom_percent = 30")
	cfg, err := config.LoadOrCreate(h.configPath)
	if err != nil || cfg.Camera.ZoomPercent != 30 {
		t.Fatalf("zoom = %v, %v", cfg.Camera.ZoomPercent, err)
	}
	h.waitFor("the list to show the new value", func(m *Model) bool {
		s, _ := m.selectedSetting()
		return s.Value == "30"
	})

	// Colour keys take hex.
	h.selectSetting("style.bg_top")
	h.press(enter)
	h.waitScreen("hex like #5C66C7, or r, g, b")
	h.typeText("purple")
	h.press(enter)
	h.waitStatus("purple is not a colour — try #5C66C7")
	h.press(enter)
	h.typeText("#5C66C7")
	h.press(enter)
	h.waitStatus("style.bg_top = [0.361, 0.400, 0.780]")

	// A value that parses but fails the extra check is rolled back too.
	before := read()
	h.selectSetting("style.background_image")
	h.press(enter)
	h.typeText("/no/such/image.png")
	h.press(enter)
	h.waitFor("a rejection", func(m *Model) bool { return strings.Contains(m.status, "does not exist") })
	if read() != before {
		t.Fatal("a missing background image was left in the config")
	}
	h.quit()
}

func TestRecordingsRenameDropRawAndDelete(t *testing.T) {
	var rendered, raw string
	h := newHarness(t, Overrides{}, func(h *harness) {
		rendered = h.makeRecording("2026-01-01_10-00-00", "capture.mp4", "telemetry.json", "export.mp4")
		raw = h.makeRecording("2026-01-02_10-00-00", "capture.mp4")
	})
	h.press(char('3'))
	h.waitScreen("2026-01-01_10-00-00  rendered")
	h.waitScreen("2026-01-02_10-00-00  raw only")

	// Rename: prefilled with the current label, which is none.
	h.press(char('n'))
	h.waitScreen("a label kept after the timestamp")
	h.typeText("demo")
	h.press(enter)
	h.waitStatus("renamed to 2026-01-01_10-00-00_demo")
	rendered += "_demo"
	if !exists(rendered) {
		t.Fatal("the folder was not renamed")
	}

	// Enter opens the rendered video.
	h.press(enter)
	h.waitFor("open", func(*Model) bool { return len(h.fakes.snapshot().opened) == 1 })
	if got := h.fakes.snapshot().opened[0]; got != filepath.Join(rendered, "export.mp4") {
		t.Errorf("opened %s", got)
	}

	// Drop raw, confirmed.
	h.press(char('p'))
	h.waitScreen(" delete raw files ")
	h.waitScreen("freeing 0.0 MB")
	h.press(char('y'))
	h.waitFor("raw files dropped", func(m *Model) bool { return strings.HasPrefix(m.status, "raw files deleted · freed") })
	if exists(filepath.Join(rendered, "capture.mp4")) || !exists(filepath.Join(rendered, "export.mp4")) {
		t.Fatal("drop raw should delete the capture and keep the export")
	}
	h.press(char('p'))
	h.waitStatus("nothing but the rendered video is left here")

	// The raw-only recording is the only copy, so it cannot be pruned or compressed.
	h.press(char('j'))
	h.press(char('p'))
	h.waitStatus("render it first — the capture is the only copy")
	h.press(char('c'))
	h.waitStatus("render first — nothing to compress yet")

	// Delete: anything but y cancels.
	h.press(char('d'))
	h.waitScreen(" delete recording ")
	h.waitScreen("moves the whole folder to the Trash")
	h.press(char('x'))
	h.waitStatus("cancelled")
	if len(h.fakes.snapshot().trashed) != 0 || !exists(raw) {
		t.Fatal("a cancelled delete deleted something")
	}
	h.press(char('d'), char('Y'))
	h.waitFor("deleted", func(m *Model) bool { return strings.HasPrefix(m.status, "moved to Trash · ") })
	if got := h.fakes.snapshot().trashed; len(got) != 1 || got[0] != raw {
		t.Fatalf("trashed %v", got)
	}
	h.waitFor("the list to shrink and keep a selection", func(m *Model) bool {
		return len(m.recordings) == 1 && m.recordingList.selected == 0
	})
	h.quit()
}

func TestRecordingWithPauseThenRender(t *testing.T) {
	zoom := 25.0
	h := newHarness(t, Overrides{Zoom: &zoom}, nil)
	h.fakes.renderGate = make(chan struct{})

	h.press(char('j'), enter) // the Safari window
	h.waitScreen(" recording ")
	h.waitScreen("Safari — Docs (1200x800) · mic: Fake Mic")
	h.waitScreen("p/space pause · enter/s stop · q stop and quit")

	h.press(char('p'))
	h.waitFor("paused", func(m *Model) bool { return m.rec != nil && m.rec.paused && m.status == "paused" })
	h.waitScreen("  paused")
	h.press(spaceKey)
	h.waitFor("resumed", func(m *Model) bool { return m.rec != nil && !m.rec.paused && m.status == "recording…" })

	h.press(enter)
	h.waitFor("the render to start", func(m *Model) bool { return m.job != nil && m.job.kind == jobRender })
	h.waitScreen("50/100 frames")
	close(h.fakes.renderGate)
	h.waitFor("the report", func(m *Model) bool { return m.reporting })

	var report string
	var dir string
	h.inspect(func(m *Model) { report, dir = m.report, m.recordings[0].Dir })
	for _, want := range []string{
		"⚠ a test warning",
		"10 frames · 20 cursor samples · 2 clicks",
		"1920x1080 · 100 frames · 2.0x realtime",
		"saved to " + filepath.Join(dir, "export.mp4"),
	} {
		if !strings.Contains(report, want) {
			t.Errorf("report lacks %q:\n%s", want, report)
		}
	}
	h.waitScreen(" done — press any key ")

	f := h.fakes.snapshot()
	if len(f.targets) != 1 || f.targets[0] != (capture.Target{Kind: capture.TargetWindow, WindowID: 42}) {
		t.Errorf("recorded %+v", f.targets)
	}
	if f.configs[0].Camera.ZoomPercent != 25 {
		t.Errorf("the --zoom override was not applied: %v", f.configs[0].Camera.ZoomPercent)
	}
	if len(f.pauses) != 1 || f.pauses[0].EndNs <= f.pauses[0].StartNs {
		t.Errorf("pauses %+v", f.pauses)
	}
	if len(f.renders) != 1 || f.renders[0].ZoomPercent == nil || *f.renders[0].ZoomPercent != 25 {
		t.Errorf("render options %+v", f.renders)
	}
	if len(f.opened) != 1 {
		t.Errorf("opened %v", f.opened)
	}

	// Any key dismisses the report.
	h.press(char('x'))
	h.waitStatus(defaultStatus)
	h.quit()
}

func TestQuittingMidRecordingFinalisesIt(t *testing.T) {
	h := newHarness(t, Overrides{}, nil)
	h.press(enter)
	h.waitScreen("Safari — Docs")
	h.press(char('q'))
	m := h.finish()
	if !strings.HasPrefix(m.farewell, "⚠ a test warning\nsaved to ") {
		t.Fatalf("farewell %q", m.farewell)
	}
	dir := strings.TrimPrefix(m.farewell, "⚠ a test warning\nsaved to ")
	if !exists(filepath.Join(dir, "capture.mp4")) {
		t.Fatal("the capture was not finalised")
	}
	if len(h.fakes.snapshot().renders) != 0 {
		t.Fatal("quitting should not render")
	}
}

func TestSecondsLimitStopsTheRecording(t *testing.T) {
	h := newHarness(t, Overrides{Seconds: 1, NoRender: true}, nil)
	h.press(enter)
	h.waitFor("the report", func(m *Model) bool { return m.reporting })
	var report string
	h.inspect(func(m *Model) { report = m.report })
	if !strings.Contains(report, "10 frames · 20 cursor samples") || !strings.Contains(report, "saved to ") {
		t.Fatalf("report %q", report)
	}
	if got := h.fakes.snapshot().targets[0]; got.Kind != capture.TargetDisplay {
		t.Errorf("target %+v", got)
	}
	h.press(char('x'))
	h.quit()
}

func TestAFailedRecordingIsReported(t *testing.T) {
	h := newHarness(t, Overrides{}, nil)
	h.fakes.recordErr = errors.New("no displays found")
	h.press(enter)
	h.waitFor("the report", func(m *Model) bool { return m.reporting && m.rec == nil })
	h.waitScreen("failed: no displays found")
	h.press(char('x'))
	h.quit()
}

func TestCompressJob(t *testing.T) {
	var dir string
	h := newHarness(t, Overrides{}, func(h *harness) {
		dir = h.makeRecording("2026-01-01_10-00-00", "capture.mp4", "export.mp4")
	})
	h.fakes.compressGate = make(chan struct{})
	h.press(char('3'), char('c'))
	h.waitScreen("target size in MB, e.g. 25")
	h.typeText("lots")
	h.press(enter)
	h.waitStatus(`"lots" is not a size in MB — try 25`)

	h.press(char('c'))
	h.typeText("25")
	h.press(enter)
	h.waitFor("compressing", func(m *Model) bool { return m.job != nil && m.job.kind == jobCompress })
	h.waitScreen(" working ")
	h.waitScreen("compressing")
	close(h.fakes.compressGate)
	h.waitFor("the report", func(m *Model) bool { return m.reporting })
	h.waitScreen("already under 25 MB — copied as-is")
	var report string
	h.inspect(func(m *Model) { report = m.report })
	if !strings.HasSuffix(report, "saved to "+filepath.Join(dir, "export-compressed.mp4")) {
		t.Errorf("report %q", report)
	}
	h.press(char('x'))
	h.quit()
}

func TestQuittingDuringAJobWaitsForIt(t *testing.T) {
	h := newHarness(t, Overrides{}, func(h *harness) {
		h.makeRecording("2026-01-01_10-00-00", "capture.mp4")
	})
	h.fakes.renderGate = make(chan struct{})
	h.press(char('3'), char('r'))
	h.waitScreen("50/100 frames")
	h.press(char('q'))
	h.waitStatus("finishing up…")
	// Still running: the program only ends once the render does.
	h.waitScreen("50/100 frames")
	close(h.fakes.renderGate)
	m := h.finish()
	if m.reporting || m.farewell != "" {
		t.Fatalf("quitting during a job should leave quietly: %q %q", m.report, m.farewell)
	}
}

func TestPreviewToggle(t *testing.T) {
	h := newHarness(t, Overrides{}, nil)
	h.press(char('w'))
	h.waitStatus("preview · Fake Cam")
	h.waitFor("a thumbnail", func(m *Model) bool { return m.thumb != nil })
	h.waitScreen("████") // the flat grey frame renders solid
	h.press(char('w'))
	h.waitStatus("preview off")
	h.waitFor("the camera to be released", func(*Model) bool {
		cams := h.fakes.snapshot().cams
		return len(cams) == 1 && cams[0].isStopped()
	})
	h.quit()
}

func TestCountdownWithWebcam(t *testing.T) {
	h := newHarness(t, Overrides{NoRender: true}, func(h *harness) {
		for k, v := range map[string]string{"webcam.enabled": "true", "webcam.preview_s": "5"} {
			if err := config.SetValue(h.configPath, k, v); err != nil {
				t.Fatal(err)
			}
		}
	})

	// Escape backs out and, since the preview was not up before, closes it again.
	h.press(enter)
	h.waitScreen("recording in 5")
	h.waitScreen("check your framing")
	h.waitFor("a thumbnail", func(m *Model) bool { return m.thumb != nil })
	h.press(esc)
	h.waitStatus("cancelled")
	h.waitFor("the camera to be released", func(m *Model) bool {
		cams := h.fakes.snapshot().cams
		return m.countdown == nil && len(cams) == 1 && cams[0].isStopped()
	})
	if len(h.fakes.snapshot().targets) != 0 {
		t.Fatal("a cancelled countdown recorded")
	}

	// With the preview up first, enter during the countdown starts at once; the framing
	// camera is released before the recording opens its own, and comes back afterwards.
	h.press(char('w'))
	h.waitStatus("preview · Fake Cam")
	h.press(enter)
	h.waitScreen("recording in")
	h.press(enter)
	h.waitFor("recording", func(m *Model) bool { return m.rec != nil })
	h.press(char('s'))
	h.waitFor("the report", func(m *Model) bool { return m.reporting })
	f := h.fakes.snapshot()
	if len(f.camsAtStart) != 1 || !f.camsAtStart[0] {
		t.Fatal("the framing camera was still open when recording started")
	}
	h.waitFor("the preview to come back", func(m *Model) bool { return m.preview != nil })
	if got := len(h.fakes.snapshot().cams); got != 3 {
		t.Errorf("opened %d cameras, want 3", got)
	}
	h.press(char('x'))
	h.quit()
}

// --- units --------------------------------------------------------------------------

func TestHealthLines(t *testing.T) {
	got := healthLines(capture.Health{Frames: 3, CursorSamples: 1, MedianGapMs: 500})
	want := []string{
		"3 frames · 1 cursor samples · 0 clicks",
		"clicks were not recorded — grant Input Monitoring for click-zoom",
		"the cursor never moved, so there is nothing to zoom toward",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got %q", got)
	}
}

func TestSummarizeCompress(t *testing.T) {
	got := summarizeCompress(render.CompressReport{
		InputBytes: 90_000_000, OutputBytes: 30_000_000, MaxBytes: 25_000_000, Reencoded: true, VideoBitrateBps: 4_000_000,
	}, 25, "/x/export-compressed.mp4")
	want := "90.0 MB → 30.0 MB · 4.0 Mbps video\nstill 30.0 MB, over the 25 MB target — try a lower size\nsaved to /x/export-compressed.mp4"
	if got != want {
		t.Fatalf("got %q", got)
	}
}

func TestRootCause(t *testing.T) {
	inner := errors.New("line 3: expected a number\n\n")
	if got := rootCause(errors.Join(inner)); got != "line 3: expected a number" {
		t.Fatalf("got %q", got)
	}
}
