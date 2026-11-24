package recordings

// The recordings tab: every recording, opened, renamed, rendered, compressed, pruned of
// its raw files or moved to the Trash.

import (
	"fmt"
	"math"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/waarispierre/recordo/internal/domain/session"
	"github.com/waarispierre/recordo/internal/features/compress"
	"github.com/waarispierre/recordo/internal/features/render"
	"github.com/waarispierre/recordo/internal/tui/kit"
)

// TabDeps are the recordings tab's side effects, injected so tests can run it against
// fakes.
type TabDeps struct {
	Render   func(src, dst string, opts render.Options) (render.Report, error)
	Compress func(src, dst string, maxBytes uint64) (compress.CompressReport, error)
	Trash    func(dir string) error
	Open     func(path string)
}

// RealTabDeps are the real thing.
func RealTabDeps() TabDeps {
	return TabDeps{
		Render:   render.Run,
		Compress: compress.CompressToSize,
		Trash:    func(dir string) error { return (&session.Session{Dir: dir}).Trash() },
		// /usr/bin/open is cheap and one-shot enough (it launches a separate GUI app and
		// returns almost immediately) to start inline rather than routing it through a job.
		Open: func(path string) { _ = exec.Command("/usr/bin/open", path).Start() },
	}
}

var tabKeys = struct {
	open, render, compress, name, dropRaw, del key.Binding
}{
	open:     kit.Bind("enter", "open", "enter"),
	render:   kit.Bind("r", "render", "r"),
	compress: kit.Bind("c", "compress", "c"),
	name:     kit.Bind("n", "name", "n"),
	dropRaw:  kit.Bind("p", "drop raw", "p"),
	del:      kit.Bind("d", "delete", "d"),
}

// editKind is what an in-progress edit popup will do with its text once confirmed.
type editKind int

const (
	// editCompressSize edits a target size in MB for the recording in dir.
	editCompressSize editKind = iota
	// editLabel edits the label that follows the recording's timestamp.
	editLabel
)

type edit struct {
	kind  editKind
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

// Tab is the recordings tab.
type Tab struct {
	kit.Base
	host      kit.Host
	overrides kit.Overrides
	deps      TabDeps
	rows      int
	width     int

	recordings []*session.Session
	list       kit.List
	editing    *edit
	confirm    *confirm
}

// NewTab reads the recordings.
func NewTab(host kit.Host, o kit.Overrides, d TabDeps) *Tab {
	t := &Tab{host: host, overrides: o, deps: d}
	t.Refresh()
	return t
}

func (t *Tab) Title() string { return "recordings" }

func (t *Tab) Keys() []key.Binding {
	k := tabKeys
	return []key.Binding{k.open, k.render, k.compress, k.name, k.dropRaw, k.del}
}

func (t *Tab) Modal() bool { return t.editing != nil || t.confirm != nil }

// Refresh re-reads the recordings and keeps the cursor on something that still exists —
// the list shrinks under it when a recording is deleted.
func (t *Tab) Refresh() {
	all, err := session.All()
	if err != nil {
		all = nil
	}
	t.recordings = all
	if len(all) == 0 {
		t.list.Selected = -1
	} else {
		t.list.Selected = min(max(t.list.Selected, 0), len(all)-1)
	}
	t.list.Scroll(t.rows - 2)
}

func (t *Tab) Update(msg tea.Msg) tea.Cmd {
	cmd := t.update(msg)
	t.list.Scroll(t.rows - 2)
	return cmd
}

func (t *Tab) update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case kit.LayoutMsg:
		t.rows, t.width = msg.PanelRows, msg.Width
	case tea.KeyPressMsg:
		switch {
		case t.confirm != nil:
			return t.confirmKey(msg)
		case t.editing != nil:
			return t.editKey(msg)
		}
		return t.key(msg)
	default:
		// Cursor blinks and the like.
		if t.editing != nil {
			var cmd tea.Cmd
			t.editing.input, cmd = t.editing.input.Update(msg)
			return cmd
		}
	}
	return nil
}

func (t *Tab) selected() (*session.Session, bool) {
	i, ok := t.list.Get()
	if !ok || i >= len(t.recordings) {
		return nil, false
	}
	return t.recordings[i], true
}

func (t *Tab) key(k tea.KeyPressMsg) tea.Cmd {
	switch {
	case key.Matches(k, kit.Down):
		t.list.Move(1, len(t.recordings))
		return nil
	case key.Matches(k, kit.Up):
		t.list.Move(-1, len(t.recordings))
		return nil
	}
	s, ok := t.selected()
	if !ok {
		return nil
	}
	switch {
	case key.Matches(k, tabKeys.render):
		return t.startRender(s.Dir)
	case key.Matches(k, tabKeys.del):
		t.confirm = &confirm{kind: confirmDelete, dir: s.Dir, bytes: s.Bytes()}
	case key.Matches(k, tabKeys.dropRaw):
		switch bytes := s.RawBytes(); {
		case bytes == 0:
			t.host.SetStatus("nothing but the rendered video is left here")
		case !s.HasExport():
			// Without an export the raw capture is the only copy there is.
			t.host.SetStatus("render it first — the capture is the only copy")
		default:
			t.confirm = &confirm{kind: confirmDropRaw, dir: s.Dir, bytes: bytes}
		}
	case key.Matches(k, tabKeys.name):
		// Prefilled, unlike the other fields: a rename is usually a tweak to the label
		// that is already there.
		label, _ := s.Label()
		t.host.SetStatus("name it — enter to rename, esc to cancel")
		return t.startEdit(editLabel, s.Dir, label)
	case key.Matches(k, tabKeys.compress):
		if !s.HasExport() {
			t.host.SetStatus("render first — nothing to compress yet")
			return nil
		}
		t.host.SetStatus("target size in MB — enter to compress, esc to cancel")
		return t.startEdit(editCompressSize, s.Dir, "")
	case key.Matches(k, tabKeys.open):
		target := s.Capture()
		if s.HasExport() {
			target = s.Export()
		}
		t.deps.Open(target)
	}
	return nil
}

func (t *Tab) startEdit(kind editKind, dir, value string) tea.Cmd {
	in := kit.NewInput(t.width)
	in.SetValue(value)
	in.CursorEnd()
	t.editing = &edit{kind: kind, dir: dir, input: in}
	return t.editing.input.Focus()
}

// confirmKey answers the confirmation popup. Anything other than an explicit yes
// cancels, so a stray keypress can never be the thing that deletes a recording.
func (t *Tab) confirmKey(k tea.KeyPressMsg) tea.Cmd {
	c := t.confirm
	t.confirm = nil
	if !key.Matches(k, kit.Yes) {
		t.host.SetStatus("cancelled")
		return nil
	}
	mb := func(b uint64) float64 { return float64(b) / 1_000_000 }
	// Built directly rather than through session.Open, which insists on a capture.mp4
	// that an already-pruned recording no longer has.
	var err error
	var done string
	switch c.kind {
	case confirmDelete:
		err = t.deps.Trash(c.dir)
		done = fmt.Sprintf("moved to Trash · %.1f MB", mb(c.bytes))
	case confirmDropRaw:
		err = (&session.Session{Dir: c.dir}).DropRaw()
		done = fmt.Sprintf("raw files deleted · freed %.1f MB", mb(c.bytes))
	}
	if err != nil {
		done = "failed: " + err.Error()
	}
	t.host.SetStatus(done)
	t.Refresh()
	return nil
}

func (t *Tab) editKey(k tea.KeyPressMsg) tea.Cmd {
	e := t.editing
	switch k.Code {
	case tea.KeyEscape:
		t.editing = nil
		t.host.SetStatus("cancelled")
		return nil
	case tea.KeyEnter:
	default:
		var cmd tea.Cmd
		e.input, cmd = e.input.Update(k)
		return cmd
	}

	t.editing = nil
	value := strings.TrimSpace(e.input.Value())
	switch e.kind {
	case editLabel:
		// Blank is how you take a label off again.
		s := &session.Session{Dir: e.dir}
		if err := s.SetLabel(value); err != nil {
			t.host.SetStatus("failed: " + err.Error())
		} else {
			t.host.SetStatus("renamed to " + filepath.Base(s.Dir))
		}
		t.Refresh()
	case editCompressSize:
		// Blank means "leave it alone".
		if value == "" {
			t.host.SetStatus("unchanged")
			return nil
		}
		mb, err := strconv.ParseFloat(value, 64)
		if err != nil || math.IsNaN(mb) || math.IsInf(mb, 0) || mb <= 0 {
			t.host.SetStatus(fmt.Sprintf("%q is not a size in MB — try 25", value))
			return nil
		}
		return t.startCompress(e.dir, mb)
	}
	return nil
}

// startRender re-renders a recording.
func (t *Tab) startRender(dir string) tea.Cmd {
	run := t.deps.Render
	opts := render.Options{ZoomPercent: t.overrides.Zoom, Webcam: t.overrides.Webcam}
	return t.host.StartJob(kit.JobRender, "rendering…", func(progress func(int, int)) (string, error) {
		s, err := session.Open(dir)
		if err != nil {
			return "", err
		}
		out := s.Export()
		opts.Progress = progress
		rep, err := run(s.Capture(), out, opts)
		if err != nil {
			return "", err
		}
		return rep.Summary() + "\nsaved to " + out, nil
	})
}

func (t *Tab) startCompress(dir string, maxMB float64) tea.Cmd {
	run := t.deps.Compress
	return t.host.StartJob(kit.JobCompress, "compressing…", func(func(int, int)) (string, error) {
		src := filepath.Join(dir, "export.mp4")
		dst := filepath.Join(dir, "export-compressed.mp4")
		rep, err := run(src, dst, uint64(maxMB*1_000_000))
		if err != nil {
			return "", err
		}
		return summarizeCompress(rep, maxMB, dst), nil
	})
}

func summarizeCompress(rep compress.CompressReport, maxMB float64, dst string) string {
	mb := func(b uint64) float64 { return float64(b) / 1_000_000 }
	var msg string
	if rep.Reencoded {
		msg = fmt.Sprintf("%.1f MB → %.1f MB", mb(rep.InputBytes), mb(rep.OutputBytes))
		if rep.VideoBitrateBps > 0 {
			msg += fmt.Sprintf(" · %.1f Mbps video", float64(rep.VideoBitrateBps)/1_000_000)
		}
	} else {
		msg = fmt.Sprintf("already under %.0f MB — copied as-is", maxMB)
	}
	if !rep.Fits() {
		msg += fmt.Sprintf("\nstill %.1f MB, over the %.0f MB target — try a lower size", mb(rep.OutputBytes), maxMB)
	}
	return msg + "\nsaved to " + dst
}

func (t *Tab) Draw(c *lipgloss.Canvas, r kit.Rect) {
	if len(t.recordings) == 0 {
		kit.List{Selected: -1}.Draw(c, r, " recordings ", []string{"no recordings yet — press 1 to make one"})
		return
	}
	items := make([]string, len(t.recordings))
	for i, s := range t.recordings {
		state := kit.Plain.Foreground(kit.Yellow).Render("raw only")
		if s.HasExport() {
			state = kit.Plain.Foreground(kit.Green).Render("rendered")
		}
		items[i] = filepath.Base(s.Dir) + "  " + state
	}
	t.list.Draw(c, r, " recordings ", items)
}

func (t *Tab) DrawPopup(c *lipgloss.Canvas, screen kit.Rect) {
	if e := t.editing; e != nil {
		switch e.kind {
		case editCompressSize:
			kit.DrawEditPopup(c, screen, "compress "+filepath.Base(e.dir), "target size in MB, e.g. 25", e.input)
		case editLabel:
			kit.DrawEditPopup(c, screen, "name", "a label kept after the timestamp · blank removes it", e.input)
		}
	}
	if cf := t.confirm; cf != nil {
		mb := float64(cf.bytes) / 1_000_000
		name := filepath.Base(cf.dir)
		switch cf.kind {
		case confirmDelete:
			kit.DrawConfirmPopup(c, screen, " delete recording ", []string{name, "",
				fmt.Sprintf("moves the whole folder to the Trash, %.1f MB", mb),
				"the rendered video goes with it"})
		case confirmDropRaw:
			kit.DrawConfirmPopup(c, screen, " delete raw files ", []string{name, "",
				fmt.Sprintf("deletes the capture and sidecars, freeing %.1f MB", mb),
				"the rendered videos stay · this one is not undoable"})
		}
	}
}
