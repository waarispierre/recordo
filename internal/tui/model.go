package tui

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/progress"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/waarispierre/recordo/internal/domain/config"
	"github.com/waarispierre/recordo/internal/domain/pauses"
	"github.com/waarispierre/recordo/internal/domain/session"
	"github.com/waarispierre/recordo/internal/features/record"
	"github.com/waarispierre/recordo/internal/platform/macos"
)

type tab int

const (
	tabRecord tab = iota
	tabSettings
	tabRecordings
)

var tabs = []tab{tabRecord, tabSettings, tabRecordings}

func (t tab) title() string {
	switch t {
	case tabSettings:
		return "settings"
	case tabRecordings:
		return "recordings"
	default:
		return "record"
	}
}

func (t tab) next() tab { return (t + 1) % 3 }
func (t tab) prev() tab { return (t + 2) % 3 }

const defaultStatus = "j/k move · enter select · tab switch · q quit"

// listState is a list's selection and scroll position. selected is -1 for none.
type listState struct {
	selected int
	offset   int
}

func (l *listState) get() (int, bool) { return l.selected, l.selected >= 0 }

// editKind is what an in-progress edit popup will do with its text once confirmed.
type editKind int

const (
	// editSetting edits a config setting selected in the Settings tab.
	editSetting editKind = iota
	// editCompressSize edits a target size in MB for the recording in dir.
	editCompressSize
	// editLabel edits the label that follows the recording's timestamp.
	editLabel
)

type edit struct {
	kind editKind
	// key is the setting being edited, for editSetting.
	key string
	// dir is the recording, for editCompressSize and editLabel.
	dir   string
	input textinput.Model
}

// confirmKind is a destructive action waiting on a yes/no answer. Nothing here is
// undoable in place — a delete goes to the Trash, but dropping the raw files is final —
// so none of it happens on a single keystroke.
type confirmKind int

const (
	// confirmDelete moves the whole recording to the Trash.
	confirmDelete confirmKind = iota
	// confirmDropRaw deletes the capture and sidecars, keeping the rendered videos.
	confirmDropRaw
)

type confirm struct {
	kind  confirmKind
	dir   string
	bytes uint64
}

type jobKind int

const (
	jobRender jobKind = iota
	jobCompress
)

// job is a render or compress running on a goroutine.
type job struct {
	kind    jobKind
	started time.Time
	// progress is written by the render's callback once per frame and read on the tick:
	// sending a message per frame would redraw the screen hundreds of times a second.
	progress *jobProgress
	// done and total are the last progress seen; total 0 means no frame has landed yet.
	done, total int
}

// countdown is the pre-roll before a webcam recording, with the framing check up.
type countdown struct {
	started    time.Time
	length     time.Duration
	target     record.Target
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
	camera   previewCamera
	paused   bool
	pausedNs uint64
	// stopped is set when the recorder ended the capture itself (the --seconds limit).
	stopped bool
}

type (
	tickMsg    struct{}
	jobDoneMsg struct {
		text string
		err  error
	}
	previewMsg struct {
		id  uint64
		cam framingCamera
		err error
	}
	recordedMsg struct {
		session  *session.Session
		health   record.Health
		warnings []string
		err      error
	}
)

// Model is the whole app. It is always used by pointer.
type Model struct {
	deps       deps
	overrides  Overrides
	configPath string
	width      int
	height     int

	tab           tab
	windows       []macos.Window // offered after "entire display"
	windowList    listState
	settings      []config.Setting
	settingList   listState
	recordings    []*session.Session
	recordingList listState

	editing   *edit
	confirm   *confirm
	job       *job
	rec       *recording
	countdown *countdown

	cams *cameraSlot
	// preview is the pre-flight camera, held open while the Record tab shows a framing
	// check. It owns the device, so a recording has to close it before opening one.
	preview         framingCamera
	previewID       uint64
	previewOpening  bool
	previewSettings config.WebcamSettings
	// thumb is the newest frame from whichever camera is on screen.
	thumb *macos.PreviewFrame

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

	keys keyMap
	help help.Model
	bar  progress.Model
	spin spinner.Model

	// bg tracks every goroutine the app started, so Run can wait for a recording or a
	// render to finish before the process exits.
	bg sync.WaitGroup
	// afterUpdate, when set, runs at the end of every Update. Tests use it to observe the
	// model on the program's own goroutine.
	afterUpdate func(*Model)
}

func newModel(o Overrides, configPath string, d deps) (*Model, error) {
	settings, err := config.Settings(configPath)
	if err != nil {
		return nil, err
	}
	m := &Model{
		deps:       d,
		overrides:  o,
		configPath: configPath,
		windows:    d.windows(),
		settings:   settings,
		cams:       &cameraSlot{},
		status:     defaultStatus,
		keys:       newKeyMap(),
		help:       newHelp(),
		bar:        newBar(),
		spin:       newSpinner(),
	}
	m.reloadRecordings()
	return m, nil
}

func (m *Model) Init() tea.Cmd { return nil }

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	cmd := m.update(msg)
	m.scroll()
	if m.afterUpdate != nil {
		m.afterUpdate(m)
	}
	return m, tea.Batch(cmd, m.ensureTick())
}

func (m *Model) update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tickMsg:
		m.ticking = false
		return m.onTick()
	case spinner.TickMsg:
		if m.job != nil && m.job.kind == jobCompress {
			var cmd tea.Cmd
			m.spin, cmd = m.spin.Update(msg)
			return cmd
		}
	case jobDoneMsg:
		return m.onJobDone(msg)
	case recordedMsg:
		return m.onRecorded(msg)
	case previewMsg:
		m.onPreview(msg)
	case tea.KeyPressMsg:
		return m.onKey(msg)
	default:
		// Cursor blinks and the like.
		if m.editing != nil {
			var cmd tea.Cmd
			m.editing.input, cmd = m.editing.input.Update(msg)
			return cmd
		}
	}
	return nil
}

// ensureTick keeps a 100ms tick running while anything on screen moves on its own.
func (m *Model) ensureTick() tea.Cmd {
	animating := m.countdown != nil || m.rec != nil || m.job != nil || m.previewUp()
	if m.ticking || !animating {
		return nil
	}
	m.ticking = true
	return tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg { return tickMsg{} })
}

func (m *Model) onTick() tea.Cmd {
	if c := m.countdown; c != nil && time.Since(c.started) >= c.length {
		return m.beginRecording(c.target, c.cfg, c.hadPreview)
	}
	if r := m.rec; r != nil {
		r.live.mu.Lock()
		r.label = r.live.label
		r.pausedFor = time.Duration(r.live.pausedNs)
		cam := r.live.camera
		if r.live.stopped && !r.stopping {
			r.stopping = true
			m.status = "stopping…"
		}
		paused := r.live.paused
		r.live.mu.Unlock()
		if paused != r.paused && !r.stopping {
			r.paused = paused
			m.status = "recording…"
			if paused {
				m.status = "paused"
			}
		}
		m.thumb = nil
		if r.preview && cam != nil {
			m.pullThumb(cam)
		}
	}
	if j := m.job; j != nil {
		j.done, j.total = j.progress.get()
	}
	if m.preview != nil && m.rec == nil {
		m.pullThumb(m.preview)
	}
	return nil
}

func (m *Model) pullThumb(cam previewCamera) {
	if f, ok := cam.Preview(); ok {
		m.thumb = &f
	}
}

// goBackground runs fn on a goroutine Run waits for, delivering its result as a message.
func (m *Model) goBackground(fn func() tea.Msg) tea.Cmd {
	ch := make(chan tea.Msg, 1)
	m.bg.Add(1)
	go func() {
		defer m.bg.Done()
		ch <- fn()
	}()
	return func() tea.Msg { return <-ch }
}

// shutdown finalises anything still running once the program has ended.
func (m *Model) shutdown() {
	if r := m.rec; r != nil && !r.stopping {
		r.stopping = true
		close(r.stop)
	}
	m.cams.bump()
	m.bg.Wait()
	m.cams.mu.Lock()
	m.cams.release()
	m.cams.mu.Unlock()
}

func (m *Model) fail(err error) tea.Cmd {
	m.fatal = err
	return tea.Quit
}

func (m *Model) reloadSettings() error {
	settings, err := config.Settings(m.configPath)
	if err != nil {
		return err
	}
	m.settings = settings
	if len(m.settings) == 0 {
		m.settingList.selected = 0
	} else {
		m.settingList.selected = min(m.settingList.selected, len(m.settings)-1)
	}
	return nil
}

// reloadRecordings re-reads the recordings and keeps the cursor on something that still
// exists — the list shrinks under it when a recording is deleted.
func (m *Model) reloadRecordings() {
	all, err := session.All()
	if err != nil {
		all = nil
	}
	m.recordings = all
	switch {
	case len(all) == 0:
		m.recordingList.selected = -1
	default:
		m.recordingList.selected = min(max(m.recordingList.selected, 0), len(all)-1)
	}
}

func (m *Model) list() (*listState, int) {
	switch m.tab {
	case tabSettings:
		return &m.settingList, len(m.settings)
	case tabRecordings:
		return &m.recordingList, len(m.recordings)
	default:
		return &m.windowList, len(m.windows) + 1
	}
}

func (m *Model) moveBy(delta int) {
	l, n := m.list()
	if n == 0 {
		return
	}
	cur := max(l.selected, 0)
	l.selected = ((cur+delta)%n + n) % n
}

// scroll keeps each list's selection inside its visible rows, the way ratatui's
// ListState does: the window only moves once the selection reaches its edge.
func (m *Model) scroll() {
	rows := max(m.bodyRows()-2, 1)
	for _, l := range []*listState{&m.windowList, &m.settingList, &m.recordingList} {
		if l.selected < 0 {
			l.offset = 0
			continue
		}
		if l.selected >= l.offset+rows {
			l.offset = l.selected - rows + 1
		}
		if l.selected < l.offset {
			l.offset = l.selected
		}
	}
}

func (m *Model) selectedRecording() (*session.Session, bool) {
	i, ok := m.recordingList.get()
	if !ok || i >= len(m.recordings) {
		return nil, false
	}
	return m.recordings[i], true
}

func (m *Model) selectedSetting() (config.Setting, bool) {
	i := m.settingList.selected
	if i < 0 || i >= len(m.settings) {
		return config.Setting{}, false
	}
	return m.settings[i], true
}

// --- keys -------------------------------------------------------------------------

func (m *Model) onKey(k tea.KeyPressMsg) tea.Cmd {
	switch {
	case m.countdown != nil:
		return m.countdownKey(k)
	case m.rec != nil:
		return m.recordingKey(k)
	case m.reporting:
		m.reporting = false
		m.report = ""
		m.status = defaultStatus
		return nil
	case m.job != nil:
		return m.jobKey(k)
	case m.confirm != nil:
		return m.confirmKey(k)
	case m.editing != nil:
		return m.editKey(k)
	}
	return m.normalKey(k)
}

// countdownKey: enter starts early, esc backs out.
func (m *Model) countdownKey(k tea.KeyPressMsg) tea.Cmd {
	c := m.countdown
	switch {
	case key.Matches(k, m.keys.abort):
		m.countdown = nil
		m.status = "cancelled"
		if !c.hadPreview {
			_, cmd := m.closePreview()
			return cmd
		}
	case key.Matches(k, m.keys.stop):
		return m.beginRecording(c.target, c.cfg, c.hadPreview)
	}
	return nil
}

func (m *Model) recordingKey(k tea.KeyPressMsg) tea.Cmd {
	r := m.rec
	if r.stopping {
		return nil
	}
	switch {
	case key.Matches(k, m.keys.pause):
		select {
		case r.toggle <- struct{}{}:
		default:
		}
		return nil
	case key.Matches(k, m.keys.abort):
		r.quit = true
	case key.Matches(k, m.keys.stop):
	default:
		return nil
	}
	// Either way the capture ends here; the recorder's teardown still runs, so the
	// recording is finalised rather than abandoned.
	r.stopping = true
	close(r.stop)
	m.status = "stopping…"
	return nil
}

// jobKey handles a keypress while a render or compress job is running. Quitting waits
// for the job, so it never leaves ffmpeg writing into a half-finished file.
func (m *Model) jobKey(k tea.KeyPressMsg) tea.Cmd {
	if key.Matches(k, m.keys.quit) && !m.quitting {
		m.quitting = true
		m.status = "finishing up…"
	}
	return nil
}

func (m *Model) normalKey(k tea.KeyPressMsg) tea.Cmd {
	keys := m.keys
	switch {
	case key.Matches(k, keys.quit):
		return tea.Quit
	case key.Matches(k, keys.down):
		m.moveBy(1)
	case key.Matches(k, keys.up):
		m.moveBy(-1)
	case key.Matches(k, keys.next):
		m.tab = m.tab.next()
	case key.Matches(k, keys.prev):
		m.tab = m.tab.prev()
	case key.Matches(k, keys.tab1):
		m.tab = tabRecord
	case key.Matches(k, keys.tab2):
		m.tab = tabSettings
	case key.Matches(k, keys.tab3):
		m.tab = tabRecordings
	case m.tab == tabRecord && key.Matches(k, keys.preview):
		return m.togglePreview()
	case m.tab == tabRecordings && key.Matches(k, keys.render):
		if s, ok := m.selectedRecording(); ok {
			return m.startRender(s.Dir)
		}
	case m.tab == tabRecordings && key.Matches(k, keys.del):
		if s, ok := m.selectedRecording(); ok {
			m.confirm = &confirm{kind: confirmDelete, dir: s.Dir, bytes: s.Bytes()}
		}
	case m.tab == tabRecordings && key.Matches(k, keys.dropRaw):
		if s, ok := m.selectedRecording(); ok {
			switch bytes := s.RawBytes(); {
			case bytes == 0:
				m.status = "nothing but the rendered video is left here"
			case !s.HasExport():
				// Without an export the raw capture is the only copy there is.
				m.status = "render it first — the capture is the only copy"
			default:
				m.confirm = &confirm{kind: confirmDropRaw, dir: s.Dir, bytes: bytes}
			}
		}
	case m.tab == tabRecordings && key.Matches(k, keys.name):
		if s, ok := m.selectedRecording(); ok {
			// Prefilled, unlike the other fields: a rename is usually a tweak to the
			// label that is already there.
			label, _ := s.Label()
			m.status = "name it — enter to rename, esc to cancel"
			return m.startEdit(editLabel, "", s.Dir, label)
		}
	case m.tab == tabRecordings && key.Matches(k, keys.compress):
		if s, ok := m.selectedRecording(); ok {
			if !s.HasExport() {
				m.status = "render first — nothing to compress yet"
				return nil
			}
			m.status = "target size in MB — enter to compress, esc to cancel"
			return m.startEdit(editCompressSize, "", s.Dir, "")
		}
	case key.Matches(k, keys.enter):
		switch m.tab {
		case tabRecord:
			target := record.Target{Kind: record.TargetDisplay}
			if i := m.windowList.selected; i > 0 && i <= len(m.windows) {
				target = record.Target{Kind: record.TargetWindow, WindowID: m.windows[i-1].ID}
			}
			return m.startRecording(target)
		case tabSettings:
			if s, ok := m.selectedSetting(); ok {
				m.status = fmt.Sprintf("editing %s — enter to save, esc to cancel", s.Key)
				return m.startEdit(editSetting, s.Key, "", "")
			}
		case tabRecordings:
			if s, ok := m.selectedRecording(); ok {
				target := s.Capture()
				if s.HasExport() {
					target = s.Export()
				}
				m.deps.open(target)
			}
		}
	}
	return nil
}

func (m *Model) startEdit(kind editKind, settingKey, dir, value string) tea.Cmd {
	in := newInput(max(m.popupWidth(60)-6, 10))
	in.SetValue(value)
	in.CursorEnd()
	m.editing = &edit{kind: kind, key: settingKey, dir: dir, input: in}
	return m.editing.input.Focus()
}

// confirmKey answers the confirmation popup. Anything other than an explicit yes
// cancels, so a stray keypress can never be the thing that deletes a recording.
func (m *Model) confirmKey(k tea.KeyPressMsg) tea.Cmd {
	c := m.confirm
	m.confirm = nil
	if !key.Matches(k, m.keys.yes) {
		m.status = "cancelled"
		return nil
	}
	mb := func(b uint64) float64 { return float64(b) / 1_000_000 }
	// Built directly rather than through session.Open, which insists on a capture.mp4
	// that an already-pruned recording no longer has.
	var err error
	var done string
	switch c.kind {
	case confirmDelete:
		err = m.deps.trash(c.dir)
		done = fmt.Sprintf("moved to Trash · %.1f MB", mb(c.bytes))
	case confirmDropRaw:
		err = (&session.Session{Dir: c.dir}).DropRaw()
		done = fmt.Sprintf("raw files deleted · freed %.1f MB", mb(c.bytes))
	}
	m.status = done
	if err != nil {
		m.status = "failed: " + err.Error()
	}
	m.reloadRecordings()
	return nil
}

func (m *Model) editKey(k tea.KeyPressMsg) tea.Cmd {
	e := m.editing
	switch k.Code {
	case tea.KeyEscape:
		m.editing = nil
		m.status = "cancelled"
		return nil
	case tea.KeyEnter:
	default:
		var cmd tea.Cmd
		e.input, cmd = e.input.Update(k)
		return cmd
	}

	m.editing = nil
	value := strings.TrimSpace(e.input.Value())
	// Blank means "leave it alone" everywhere except a label, where it is how you take
	// one off again.
	if value == "" && e.kind != editLabel {
		m.status = "unchanged"
		return nil
	}
	switch e.kind {
	case editSetting:
		return m.commitSetting(e.key, value)
	case editLabel:
		s := &session.Session{Dir: e.dir}
		if err := s.SetLabel(value); err != nil {
			m.status = "failed: " + err.Error()
		} else {
			m.status = "renamed to " + filepath.Base(s.Dir)
		}
		m.reloadRecordings()
	case editCompressSize:
		mb, err := strconv.ParseFloat(value, 64)
		if err != nil || math.IsNaN(mb) || math.IsInf(mb, 0) || mb <= 0 {
			m.status = fmt.Sprintf("%q is not a size in MB — try 25", value)
			return nil
		}
		return m.startCompress(e.dir, mb)
	}
	return nil
}

// commitSetting writes one setting, verifies it, and rolls back. Same contract as the
// non-interactive path: an invalid value must never be left in the file.
func (m *Model) commitSetting(settingKey, value string) tea.Cmd {
	previous := ""
	for _, s := range m.settings {
		if s.Key == settingKey {
			previous = s.Value
		}
	}
	if config.IsColourKey(settingKey) {
		v, ok := config.RGBToTOML(value)
		if !ok {
			m.status = fmt.Sprintf("%s is not a colour — try #5C66C7", value)
			return nil
		}
		value = v
	}

	before, err := os.ReadFile(m.configPath)
	if err != nil {
		return m.fail(err)
	}
	err = config.SetValue(m.configPath, settingKey, value)
	if err == nil {
		var cfg config.Config
		if cfg, err = config.LoadOrCreate(m.configPath); err == nil {
			_, err = cfg.BackgroundPath()
		}
	}
	if err != nil {
		if werr := os.WriteFile(m.configPath, before, 0o644); werr != nil {
			return m.fail(werr)
		}
		m.status = fmt.Sprintf("rejected: %s — keeping %s", rootCause(err), previous)
		return nil
	}
	m.status = fmt.Sprintf("%s = %s", settingKey, value)
	if err := m.reloadSettings(); err != nil {
		return m.fail(err)
	}
	return nil
}

// rootCause is the innermost error's last non-blank line: the part that says what was
// actually wrong, without the "parse <path>:" wrapping around it.
func rootCause(err error) string {
	for u := errors.Unwrap(err); u != nil; u = errors.Unwrap(u) {
		err = u
	}
	lines := strings.Split(err.Error(), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			return l
		}
	}
	return "invalid"
}

// --- preview ------------------------------------------------------------------------

func (m *Model) previewUp() bool { return m.preview != nil || m.previewOpening }

// togglePreview opens or closes the pre-flight camera behind the Record tab's framing
// check.
func (m *Model) togglePreview() tea.Cmd {
	if closed, cmd := m.closePreview(); closed {
		m.status = "preview off"
		return cmd
	}
	cfg, err := config.LoadOrCreate(m.configPath)
	if err != nil {
		m.status = "could not load config: " + err.Error()
		return nil
	}
	return m.openPreview(cfg)
}

// openPreview opens the camera for a framing check. A camera that is missing, in use or
// denied is a "no preview", not an error worth derailing the app over — the same call
// degrades a recording to screen-only rather than failing it.
func (m *Model) openPreview(cfg config.Config) tea.Cmd {
	id := m.cams.bump()
	m.previewID = id
	m.previewOpening = true
	m.previewSettings = cfg.ClampedWebcam()
	m.thumb = nil
	cams, open, device := m.cams, m.deps.openCamera, cfg.Webcam.Device
	return m.goBackground(func() tea.Msg {
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
func (m *Model) closePreview() (bool, tea.Cmd) {
	if !m.previewUp() {
		return false, nil
	}
	id := m.cams.bump()
	m.preview, m.previewOpening, m.thumb = nil, false, nil
	cams := m.cams
	return true, m.goBackground(func() tea.Msg {
		cams.mu.Lock()
		defer cams.mu.Unlock()
		// A newer open has already replaced (and released) this camera.
		if cams.wanted(id) {
			cams.release()
		}
		return nil
	})
}

func (m *Model) onPreview(msg previewMsg) {
	if msg.id != m.previewID || !m.previewOpening {
		// No longer wanted; whoever changed their mind released it.
		return
	}
	m.previewOpening = false
	if msg.err != nil {
		m.status = "no preview: " + msg.err.Error()
		return
	}
	m.preview = msg.cam
	m.status = "preview · " + msg.cam.Name()
}

// --- recording ----------------------------------------------------------------------

// startRecording applies the per-run overrides and either starts the pre-roll or the
// recording itself.
func (m *Model) startRecording(target record.Target) tea.Cmd {
	cfg, err := config.LoadOrCreate(m.configPath)
	if err != nil {
		m.status = "could not load config: " + err.Error()
		return nil
	}
	if z := m.overrides.Zoom; z != nil {
		cfg.Camera.ZoomPercent = *z
	}
	if mic := m.overrides.Mic; mic != nil {
		cfg.Audio.Microphone = *mic
	}
	if w := m.overrides.Webcam; w != nil {
		cfg.Webcam.Enabled = *w
	}

	// With the webcam on, hold the framing check up for a moment before anything is
	// captured: the camera needs a beat to settle on exposure anyway, and it is the last
	// chance to notice you are off-centre. Runs before the session folder is created, so
	// backing out here leaves nothing behind.
	hadPreview := m.previewUp()
	if preRoll := preRoll(cfg); cfg.Webcam.Enabled && preRoll > 0 {
		var cmd tea.Cmd
		if !hadPreview {
			cmd = m.openPreview(cfg)
		}
		m.countdown = &countdown{started: time.Now(), length: preRoll, target: target, cfg: cfg, hadPreview: hadPreview}
		return cmd
	}
	return m.beginRecording(target, cfg, hadPreview)
}

func preRoll(cfg config.Config) time.Duration {
	return time.Duration(float64(cfg.ClampedWebcam().PreviewS) * float64(time.Second))
}

func (m *Model) beginRecording(target record.Target, cfg config.Config, hadPreview bool) tea.Cmd {
	m.countdown = nil
	s, err := session.Create()
	if err != nil {
		m.status = "could not start a recording: " + err.Error()
		return nil
	}

	// The recording opens its own session on the camera, and the device only tolerates
	// one, so the framing check has to let go first: the recorder's goroutine releases it
	// under the camera lock before it starts. It is restored afterwards if it was up.
	m.cams.bump()
	m.preview, m.previewOpening, m.thumb = nil, false, nil

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
	m.rec = r
	m.status = "recording…"

	cams, doRecord, health, seconds := m.cams, m.deps.record, m.deps.health, m.overrides.Seconds
	return m.goBackground(func() tea.Msg {
		cams.mu.Lock()
		defer cams.mu.Unlock()
		cams.release()

		var warnings []string
		err := doRecord(s, target, cfg, record.RecordOptions{
			// The TUI resolves a concrete target before it ever gets here, so the chooser
			// — only invoked for TargetAsk — never runs.
			Choose: func([]macos.Window) (macos.Window, bool, error) { return macos.Window{}, false, nil },
			OnStart: func(p *record.Plan) {
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
			Stop: func(log *pauses.Log) { waitForStop(log, r.stop, r.toggle, live, seconds) },
			// Printing would scribble over the screen; they go into the report instead.
			Warn:          func(w record.Warning) { warnings = append(warnings, string(w)) },
			WebcamPreview: withPreview,
		})
		msg := recordedMsg{session: s, warnings: warnings, err: err}
		if err == nil {
			msg.health, msg.err = health(s)
		}
		return msg
	})
}

// waitForStop is the recorder's Stop callback: it returns when stop is closed or the
// --seconds limit is reached, servicing pause toggles meanwhile. It is the only code that
// touches the pause log.
func waitForStop(log *pauses.Log, stop <-chan struct{}, toggle <-chan struct{}, live *liveRecording, seconds uint) {
	started := time.Now()
	t := time.NewTicker(50 * time.Millisecond)
	defer t.Stop()
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
		case <-t.C:
		}
	}
}

func (m *Model) onRecorded(msg recordedMsg) tea.Cmd {
	r := m.rec
	m.rec, m.thumb = nil, nil
	var cmds []tea.Cmd
	if r.hadPreview && !r.quit {
		cmds = append(cmds, m.togglePreview())
	}
	warn := warningLines(msg.warnings)
	switch {
	case msg.err != nil:
		m.showReport(append(warn, "failed: "+msg.err.Error())...)
	case r.quit:
		m.showReport(append(warn, "saved to "+msg.session.Dir)...)
	case m.overrides.NoRender:
		lines := append(warn, healthLines(msg.health)...)
		m.showReport(append(lines, "saved to "+msg.session.Dir)...)
		m.reloadRecordings()
	default:
		cmds = append(cmds, m.startRenderOf(msg.session, msg.health, warn))
	}
	if r.quit {
		m.farewell = m.report
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

func (m *Model) showReport(lines ...string) {
	m.report = strings.Join(lines, "\n")
	m.reporting = true
	m.status = "press any key to dismiss"
}

// --- jobs ---------------------------------------------------------------------------

func (m *Model) startJob(kind jobKind, status string, run func(progress func(done, total int)) (string, error)) tea.Cmd {
	p := &jobProgress{}
	m.job = &job{kind: kind, started: time.Now(), progress: p}
	m.status = status
	cmd := m.goBackground(func() tea.Msg {
		text, err := run(p.set)
		return jobDoneMsg{text: text, err: err}
	})
	if kind == jobCompress {
		return tea.Batch(cmd, m.spin.Tick)
	}
	return cmd
}

func (m *Model) onJobDone(msg jobDoneMsg) tea.Cmd {
	m.job = nil
	if m.quitting {
		return tea.Quit
	}
	if msg.err != nil {
		m.showReport("failed: " + msg.err.Error())
	} else {
		m.showReport(msg.text)
	}
	m.reloadRecordings()
	return nil
}
