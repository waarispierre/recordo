package record

// The record tab: window picker, framing preview, countdown and the recording itself.
//
// Recording runs on a goroutine. Unlike the Rust version, which had to keep the capture
// on the main thread because tearing an AVCaptureSession down from a spawned thread hung
// forever, the Go platform package marshals camera work onto the main run loop itself
// (macos.Main), so the tab can treat a recording like any other background job. Its Stop
// callback blocks on a channel the tab closes, and services pause toggles sent over
// another, so the pause log is only ever touched from the recorder's own goroutine.

import (
	"fmt"
	"math"
	"os/exec"
	"strings"
	"sync"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/waarispierre/recordo/internal/domain/config"
	"github.com/waarispierre/recordo/internal/domain/pauses"
	"github.com/waarispierre/recordo/internal/domain/session"
	"github.com/waarispierre/recordo/internal/features/render"
	"github.com/waarispierre/recordo/internal/platform/macos"
	"github.com/waarispierre/recordo/internal/tui/kit"
)

// TabDeps are the record tab's side effects, injected so tests can run it against fakes:
// the sandbox the tests run in has no screen recording, camera or GPU.
type TabDeps struct {
	Windows    func() []macos.Window
	Record     func(*session.Session, Target, config.Config, RecordOptions) error
	Health     func(*session.Session) (Health, error)
	Render     func(src, dst string, opts render.Options) (render.Report, error)
	OpenCamera func(device string) (FramingCamera, error)
	Open       func(path string)
}

// RealTabDeps are the real thing.
func RealTabDeps() TabDeps {
	return TabDeps{
		Windows: func() []macos.Window {
			content, err := macos.ShareableContent()
			if err != nil {
				return nil
			}
			return Recordable(content)
		},
		Record: Record,
		Health: CheckHealth,
		Render: render.Run,
		OpenCamera: func(device string) (FramingCamera, error) {
			cam, err := macos.StartWebcam(device, "", true)
			if err != nil {
				return nil, err
			}
			return webcam{cam}, nil
		},
		// /usr/bin/open is cheap and one-shot enough (it launches a separate GUI app and
		// returns almost immediately) to start inline rather than routing it through a job.
		Open: func(path string) { _ = exec.Command("/usr/bin/open", path).Start() },
	}
}

var tabKeys = struct {
	record, preview, pause, stop, abort key.Binding
}{
	record:  kit.Bind("enter", "record", "enter"),
	preview: kit.Bind("w", "preview", "w"),
	pause:   kit.Bind("p/space", "pause", "p", "space"),
	stop:    kit.Bind("enter/s", "stop", "enter", "s"),
	abort:   kit.Bind("q", "stop and quit", "q", "esc", "ctrl+c"),
}

// countdown is the pre-roll before a webcam recording, with the framing check up.
type countdown struct {
	started    time.Time
	length     time.Duration
	target     Target
	cfg        config.Config
	hadPreview bool
}

// recording is the capture in flight, as the UI sees it.
type recording struct {
	started time.Time
	live    *liveRecording
	// stop is closed to end the recording; toggle asks the Stop callback to pause or
	// resume. Only that callback touches the pause log.
	stop   chan struct{}
	toggle chan struct{}
	// The rest mirror live, copied on each tick for drawing.
	label     string
	paused    bool
	pausedFor time.Duration
	stopping  bool
	// preview is set when the webcam overlay stays up while recording, with the settings
	// it is drawn with.
	preview    bool
	settings   config.WebcamSettings
	hadPreview bool
	// quit ends the app once the recording is finalised.
	quit bool
}

// recorded is the time that will make it into the render — wall time less the paused
// spans.
func (r *recording) recorded() time.Duration {
	return max(time.Since(r.started)-r.pausedFor, 0)
}

// liveRecording is shared between the recorder's goroutine and the UI.
type liveRecording struct {
	mu       sync.Mutex
	label    string
	camera   PreviewCamera
	paused   bool
	pausedNs uint64
	// stopped is set when the recorder ended the capture itself (the --seconds limit).
	stopped bool
}

type (
	previewMsg struct {
		id  uint64
		cam FramingCamera
		err error
	}
	recordedMsg struct {
		session  *session.Session
		health   Health
		warnings []string
		err      error
	}
)

// Tab is the record tab.
type Tab struct {
	kit.Base
	host       kit.Host
	overrides  kit.Overrides
	configPath string
	deps       TabDeps
	rows       int

	windows []macos.Window // offered after "entire display"
	list    kit.List

	countdown *countdown
	rec       *recording

	cams *cameraSlot
	// preview is the pre-flight camera, held open while the tab shows a framing check. It
	// owns the device, so a recording has to close it before opening one.
	preview         FramingCamera
	previewID       uint64
	previewOpening  bool
	previewSettings config.WebcamSettings
	// thumb is the newest frame from whichever camera is on screen.
	thumb *macos.PreviewFrame
}

// NewTab lists the windows on screen, once, as the Rust version did.
func NewTab(host kit.Host, o kit.Overrides, configPath string, d TabDeps) *Tab {
	return &Tab{host: host, overrides: o, configPath: configPath, deps: d, windows: d.Windows(), cams: &cameraSlot{}}
}

func (t *Tab) Title() string { return "record" }

func (t *Tab) Keys() []key.Binding {
	return []key.Binding{tabKeys.record, tabKeys.preview, kit.Switch, kit.Quit}
}

func (t *Tab) Capturing() bool { return t.countdown != nil || t.rec != nil }

func (t *Tab) Animating() bool { return t.Capturing() || t.previewUp() }

func (t *Tab) Badge() string {
	rec := t.rec
	if rec == nil {
		return ""
	}
	// ~1s on/off — a pulse fast enough to read as "live" without being distracting.
	lit := time.Since(rec.started).Milliseconds()/500%2 == 0
	switch {
	case rec.paused:
		return kit.Plain.Foreground(kit.Yellow).Render("❚❚")
	case lit:
		return kit.Plain.Foreground(kit.Red).Render("●")
	default:
		return kit.Dim.Render("●")
	}
}

// Stop ends a recording still in flight when the app is ending, so it is finalised
// rather than abandoned.
func (t *Tab) Stop() {
	if r := t.rec; r != nil && !r.stopping {
		r.stopping = true
		close(r.stop)
	}
	t.cams.bump()
}

// Close releases the framing camera.
func (t *Tab) Close() {
	t.cams.mu.Lock()
	t.cams.release()
	t.cams.mu.Unlock()
}

func (t *Tab) Update(msg tea.Msg) tea.Cmd {
	cmd := t.update(msg)
	t.list.Scroll(t.rows - 2)
	return cmd
}

func (t *Tab) update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case kit.LayoutMsg:
		t.rows = msg.PanelRows
	case kit.TickMsg:
		return t.onTick()
	case previewMsg:
		t.onPreview(msg)
	case recordedMsg:
		return t.onRecorded(msg)
	case tea.KeyPressMsg:
		switch {
		case t.countdown != nil:
			return t.countdownKey(msg)
		case t.rec != nil:
			return t.recordingKey(msg)
		case key.Matches(msg, kit.Down):
			t.list.Move(1, len(t.windows)+1)
		case key.Matches(msg, kit.Up):
			t.list.Move(-1, len(t.windows)+1)
		case key.Matches(msg, tabKeys.preview):
			return t.togglePreview()
		case key.Matches(msg, tabKeys.record):
			target := Target{Kind: TargetDisplay}
			if i := t.list.Selected; i > 0 && i <= len(t.windows) {
				target = Target{Kind: TargetWindow, WindowID: t.windows[i-1].ID}
			}
			return t.startRecording(target)
		}
	}
	return nil
}

func (t *Tab) onTick() tea.Cmd {
	if c := t.countdown; c != nil && time.Since(c.started) >= c.length {
		return t.beginRecording(c.target, c.cfg, c.hadPreview)
	}
	if r := t.rec; r != nil {
		r.live.mu.Lock()
		r.label = r.live.label
		r.pausedFor = time.Duration(r.live.pausedNs)
		cam := r.live.camera
		if r.live.stopped && !r.stopping {
			r.stopping = true
			t.host.SetStatus("stopping…")
		}
		paused := r.live.paused
		r.live.mu.Unlock()
		if paused != r.paused && !r.stopping {
			r.paused = paused
			if paused {
				t.host.SetStatus("paused")
			} else {
				t.host.SetStatus("recording…")
			}
		}
		t.thumb = nil
		if r.preview && cam != nil {
			t.pullThumb(cam)
		}
	}
	if t.preview != nil && t.rec == nil {
		t.pullThumb(t.preview)
	}
	return nil
}

func (t *Tab) pullThumb(cam PreviewCamera) {
	if f, ok := cam.Preview(); ok {
		t.thumb = &f
	}
}

// countdownKey: enter starts early, esc backs out.
func (t *Tab) countdownKey(k tea.KeyPressMsg) tea.Cmd {
	c := t.countdown
	switch {
	case key.Matches(k, tabKeys.abort):
		t.countdown = nil
		t.host.SetStatus("cancelled")
		if !c.hadPreview {
			_, cmd := t.closePreview()
			return cmd
		}
	case key.Matches(k, tabKeys.stop):
		return t.beginRecording(c.target, c.cfg, c.hadPreview)
	}
	return nil
}

func (t *Tab) recordingKey(k tea.KeyPressMsg) tea.Cmd {
	r := t.rec
	if r.stopping {
		return nil
	}
	switch {
	case key.Matches(k, tabKeys.pause):
		select {
		case r.toggle <- struct{}{}:
		default:
		}
		return nil
	case key.Matches(k, tabKeys.abort):
		r.quit = true
	case key.Matches(k, tabKeys.stop):
	default:
		return nil
	}
	// Either way the capture ends here; the recorder's teardown still runs, so the
	// recording is finalised rather than abandoned.
	r.stopping = true
	close(r.stop)
	t.host.SetStatus("stopping…")
	return nil
}

// --- preview ------------------------------------------------------------------------

func (t *Tab) previewUp() bool { return t.preview != nil || t.previewOpening }

// togglePreview opens or closes the pre-flight camera behind the framing check.
func (t *Tab) togglePreview() tea.Cmd {
	if closed, cmd := t.closePreview(); closed {
		t.host.SetStatus("preview off")
		return cmd
	}
	cfg, err := config.LoadOrCreate(t.configPath)
	if err != nil {
		t.host.SetStatus("could not load config: " + err.Error())
		return nil
	}
	return t.openPreview(cfg)
}

// openPreview opens the camera for a framing check. A camera that is missing, in use or
// denied is a "no preview", not an error worth derailing the app over — the same call
// degrades a recording to screen-only rather than failing it.
func (t *Tab) openPreview(cfg config.Config) tea.Cmd {
	id := t.cams.bump()
	t.previewID = id
	t.previewOpening = true
	t.previewSettings = cfg.ClampedWebcam()
	t.thumb = nil
	cams, open, device := t.cams, t.deps.OpenCamera, cfg.Webcam.Device
	return t.host.Background(func() tea.Msg {
		cams.mu.Lock()
		defer cams.mu.Unlock()
		if !cams.wanted(id) {
			return nil
		}
		cams.release()
		cam, err := open(device)
		if err != nil {
			return previewMsg{id: id, err: err}
		}
		if !cams.wanted(id) {
			cam.Stop()
			return nil
		}
		cams.open = cam
		return previewMsg{id: id, cam: cam}
	})
}

// closePreview releases the camera if the framing check holds it, or is opening it.
// Returns whether it did. The release itself happens on a goroutine, but under the
// camera lock, so a recording started next cannot open the device before it is free.
func (t *Tab) closePreview() (bool, tea.Cmd) {
	if !t.previewUp() {
		return false, nil
	}
	id := t.cams.bump()
	t.preview, t.previewOpening, t.thumb = nil, false, nil
	cams := t.cams
	return true, t.host.Background(func() tea.Msg {
		cams.mu.Lock()
		defer cams.mu.Unlock()
		// A newer open has already replaced (and released) this camera.
		if cams.wanted(id) {
			cams.release()
		}
		return nil
	})
}

func (t *Tab) onPreview(msg previewMsg) {
	if msg.id != t.previewID || !t.previewOpening {
		// No longer wanted; whoever changed their mind released it.
		return
	}
	t.previewOpening = false
	if msg.err != nil {
		t.host.SetStatus("no preview: " + msg.err.Error())
		return
	}
	t.preview = msg.cam
	t.host.SetStatus("preview · " + msg.cam.Name())
}

// --- recording ----------------------------------------------------------------------

// startRecording applies the per-run overrides and either starts the pre-roll or the
// recording itself.
func (t *Tab) startRecording(target Target) tea.Cmd {
	cfg, err := config.LoadOrCreate(t.configPath)
	if err != nil {
		t.host.SetStatus("could not load config: " + err.Error())
		return nil
	}
	if z := t.overrides.Zoom; z != nil {
		cfg.Camera.ZoomPercent = *z
	}
	if mic := t.overrides.Mic; mic != nil {
		cfg.Audio.Microphone = *mic
	}
	if w := t.overrides.Webcam; w != nil {
		cfg.Webcam.Enabled = *w
	}

	// With the webcam on, hold the framing check up for a moment before anything is
	// captured: the camera needs a beat to settle on exposure anyway, and it is the last
	// chance to notice you are off-centre. Runs before the session folder is created, so
	// backing out here leaves nothing behind.
	hadPreview := t.previewUp()
	if preRoll := preRoll(cfg); cfg.Webcam.Enabled && preRoll > 0 {
		var cmd tea.Cmd
		if !hadPreview {
			cmd = t.openPreview(cfg)
		}
		t.countdown = &countdown{started: time.Now(), length: preRoll, target: target, cfg: cfg, hadPreview: hadPreview}
		return cmd
	}
	return t.beginRecording(target, cfg, hadPreview)
}

func preRoll(cfg config.Config) time.Duration {
	return time.Duration(float64(cfg.ClampedWebcam().PreviewS) * float64(time.Second))
}

func (t *Tab) beginRecording(target Target, cfg config.Config, hadPreview bool) tea.Cmd {
	t.countdown = nil
	s, err := session.Create()
	if err != nil {
		t.host.SetStatus("could not start a recording: " + err.Error())
		return nil
	}

	// The recording opens its own session on the camera, and the device only tolerates
	// one, so the framing check has to let go first: the recorder's goroutine releases it
	// under the camera lock before it starts. It is restored afterwards if it was up.
	t.cams.bump()
	t.preview, t.previewOpening, t.thumb = nil, false, nil

	// The same setting governs the overlay that stays up while recording: turning the
	// framing preview off should mean no preview anywhere, not just no pre-roll. With it
	// off the camera's pixel tap is never added to the session either.
	withPreview := cfg.Webcam.Enabled && preRoll(cfg) > 0
	live := &liveRecording{}
	r := &recording{
		started:    time.Now(),
		live:       live,
		stop:       make(chan struct{}),
		toggle:     make(chan struct{}, 8),
		preview:    withPreview,
		settings:   cfg.ClampedWebcam(),
		hadPreview: hadPreview,
	}
	t.rec = r
	t.host.SetStatus("recording…")

	cams, doRecord, checkHealth, seconds := t.cams, t.deps.Record, t.deps.Health, t.overrides.Seconds
	return t.host.Background(func() tea.Msg {
		cams.mu.Lock()
		defer cams.mu.Unlock()
		cams.release()

		var warnings []string
		err := doRecord(s, target, cfg, RecordOptions{
			// The TUI resolves a concrete target before it ever gets here, so the chooser
			// — only invoked for TargetAsk — never runs.
			Choose: func([]macos.Window) (macos.Window, bool, error) { return macos.Window{}, false, nil },
			OnStart: func(p *Plan) {
				parts := []string{p.Label}
				if p.Microphone != "" {
					parts = append(parts, "mic: "+p.Microphone)
				}
				if p.Webcam != "" {
					parts = append(parts, "webcam: "+p.Webcam)
				}
				live.mu.Lock()
				live.label = strings.Join(parts, " · ")
				if withPreview && p.Camera != nil {
					live.camera = p.Camera
				}
				live.mu.Unlock()
			},
			Stop: func(log *pauses.Log) { serveStop(log, r.stop, r.toggle, live, seconds) },
			// Printing would scribble over the screen; they go into the report instead.
			Warn:          func(w Warning) { warnings = append(warnings, string(w)) },
			WebcamPreview: withPreview,
		})
		msg := recordedMsg{session: s, warnings: warnings, err: err}
		if err == nil {
			msg.health, msg.err = checkHealth(s)
		}
		return msg
	})
}

// serveStop is the recorder's Stop callback: it returns when stop is closed or the
// --seconds limit is reached, servicing pause toggles meanwhile. It is the only code that
// touches the pause log.
func serveStop(log *pauses.Log, stop <-chan struct{}, toggle <-chan struct{}, live *liveRecording, seconds uint) {
	started := time.Now()
	tk := time.NewTicker(50 * time.Millisecond)
	defer tk.Stop()
	for {
		pausedNs := log.PausedNs()
		live.mu.Lock()
		live.paused, live.pausedNs = log.IsPaused(), pausedNs
		live.mu.Unlock()
		// --seconds counts what ends up in the video, so a pause extends it.
		if seconds > 0 && time.Since(started)-time.Duration(pausedNs) >= time.Duration(seconds)*time.Second {
			live.mu.Lock()
			live.stopped = true
			live.mu.Unlock()
			return
		}
		select {
		case <-stop:
			return
		case <-toggle:
			log.Toggle()
		case <-tk.C:
		}
	}
}

func (t *Tab) onRecorded(msg recordedMsg) tea.Cmd {
	r := t.rec
	t.rec, t.thumb = nil, nil
	var cmds []tea.Cmd
	if r.hadPreview && !r.quit {
		cmds = append(cmds, t.togglePreview())
	}
	lines := warningLines(msg.warnings)
	switch {
	case msg.err != nil:
		lines = append(lines, "failed: "+msg.err.Error())
		t.host.ShowReport(lines...)
	case r.quit:
		lines = append(lines, "saved to "+msg.session.Dir)
		t.host.ShowReport(lines...)
	case t.overrides.NoRender:
		lines = append(append(lines, healthLines(msg.health)...), "saved to "+msg.session.Dir)
		t.host.ShowReport(lines...)
		t.host.Changed()
	default:
		cmds = append(cmds, t.startRenderOf(msg.session, msg.health, lines))
	}
	if r.quit {
		t.host.Farewell(strings.Join(lines, "\n"))
		return tea.Quit
	}
	return tea.Batch(cmds...)
}

func warningLines(ws []string) []string {
	var out []string
	for _, w := range ws {
		out = append(out, "⚠ "+w)
	}
	return out
}

// startRenderOf renders a recording that was just made, then opens it. Unlike the Rust
// version, a one-off --webcam/--no-webcam carries into this render, as it does on the CLI
// path.
func (t *Tab) startRenderOf(s *session.Session, h Health, warnings []string) tea.Cmd {
	run, noOpen, open := t.deps.Render, t.overrides.NoOpen, t.deps.Open
	opts := render.Options{ZoomPercent: t.overrides.Zoom, Webcam: t.overrides.Webcam}
	return t.host.StartJob(kit.JobRender, "rendering…", func(progress func(int, int)) (string, error) {
		out := s.Export()
		opts.Progress = progress
		rep, err := run(s.Capture(), out, opts)
		if err != nil {
			return "", err
		}
		if !noOpen {
			open(out)
		}
		lines := append(append([]string(nil), warnings...), summarizeRecord(h, rep, out))
		return strings.Join(lines, "\n"), nil
	})
}

// summarizeRecord is what the CLI's health report and render summary print, condensed
// into the popup shown when a job finishes — that detail must not silently vanish now
// that nothing prints to a plain terminal.
func summarizeRecord(h Health, rep render.Report, out string) string {
	lines := healthLines(h)
	lines = append(lines, rep.Summary())
	if rep.CropSource == render.CropFixedGuess {
		lines = append(lines, fmt.Sprintf("%s page bounds unavailable — cropped by a fixed guess, so tabs may show", rep.AppName))
	}
	lines = append(lines, "saved to "+out)
	return strings.Join(lines, "\n")
}

// healthLines is the cursor/click sanity check the CLI prints after a recording.
func healthLines(h Health) []string {
	lines := []string{fmt.Sprintf("%d frames · %d cursor samples · %d clicks", h.Frames, h.CursorSamples, h.Clicks)}
	gapOK := !math.IsNaN(h.MedianGapMs) && !math.IsInf(h.MedianGapMs, 0) && h.MedianGapMs < 100
	if !gapOK && h.CursorSamples > 10 {
		lines = append(lines, fmt.Sprintf("cursor timing is off by %.0fms — zoom may lag the pointer", h.MedianGapMs))
	}
	if !h.TapInstalled {
		lines = append(lines, "clicks were not recorded — grant Input Monitoring for click-zoom")
	}
	if h.CursorSamples <= 1 {
		lines = append(lines, "the cursor never moved, so there is nothing to zoom toward")
	}
	return lines
}

// --- drawing ------------------------------------------------------------------------

func (t *Tab) Draw(c *lipgloss.Canvas, area kit.Rect) {
	switch {
	case t.countdown != nil:
		t.drawCountdown(c, area)
	case t.rec != nil:
		t.drawRecording(c, area)
	default:
		labels := []string{"entire display"}
		for _, w := range t.windows {
			labels = append(labels, Label(w))
		}
		t.list.Draw(c, area, " what to record ", labels)
		t.drawPreview(c, area)
	}
}

// drawPreview is the pre-flight framing check, in the corner the webcam settings put it.
func (t *Tab) drawPreview(c *lipgloss.Canvas, area kit.Rect) {
	if !t.previewUp() {
		return
	}
	if t.thumb != nil {
		drawWebcamPip(c, area, t.previewSettings, *t.thumb)
		return
	}
	// The camera takes a moment to deliver its first frame; saying so beats a corner that
	// just sits empty.
	spot := kit.Rect{X: area.X + 2, Y: max(area.Bottom()-2, area.Y), W: min(max(area.W-4, 0), 20), H: 1}
	kit.Put(c, spot, kit.Dim.Render("waking the camera…"))
}

// drawCountdown is the pre-roll: what is about to be recorded, how long is left, and the
// camera.
func (t *Tab) drawCountdown(c *lipgloss.Canvas, r kit.Rect) {
	cd := t.countdown
	inner := kit.DrawBlock(c, r, " starting ", kit.Plain.Foreground(kit.Yellow))
	left := max(cd.length-time.Since(cd.started), 0)
	// Round up, so a full second is on screen for each number rather than flashing 0.
	seconds := int64(left / time.Second)
	if left%time.Second >= time.Millisecond {
		seconds++
	}
	kit.Put(c, inner, strings.Join([]string{
		kit.Dim.Render("● ") + kit.Bold.Render(fmt.Sprintf("recording in %d", seconds)),
		kit.Dim.Render("check your framing"),
		kit.Dim.Render("enter start now · esc cancel"),
	}, "\n"))
	t.drawPreview(c, inner)
}

// drawRecording is the panel shown while a capture is in flight: elapsed time, what is
// being recorded, and — when the webcam is on — a live thumbnail in the configured
// corner.
func (t *Tab) drawRecording(c *lipgloss.Canvas, r kit.Rect) {
	rec := t.rec
	inner := kit.DrawBlock(c, r, " recording ", kit.Plain)
	elapsed := rec.recorded()
	secs := int64(elapsed / time.Second)
	first := kit.Plain.Foreground(kit.Red).Render("● ")
	if rec.paused {
		first = kit.Plain.Foreground(kit.Yellow).Render("❚❚ ")
	}
	first += fmt.Sprintf("%02d:%02d", secs/60, secs%60)
	if rec.paused {
		first += kit.Plain.Foreground(kit.Yellow).Render("  paused")
	}
	lines := []string{first}
	if rec.label != "" {
		lines = append(lines, kit.Dim.Render(rec.label))
	}
	hint := "p/space pause · enter/s stop · q stop and quit"
	if rec.stopping {
		hint = "stopping — finishing the file"
	}
	lines = append(lines, kit.Dim.Render(hint))
	kit.Put(c, inner, strings.Join(lines, "\n"))

	// Drawn last so the overlay sits on top of the mark rather than under it.
	if rec.preview && t.thumb != nil {
		drawWebcamPip(c, inner, rec.settings, *t.thumb)
	}
}
