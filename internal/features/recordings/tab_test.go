package recordings_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/waarispierre/recordo/internal/tui"
	tt "github.com/waarispierre/recordo/internal/tui/tuitest"
)

func TestRecordingsRenameDropRawAndDelete(t *testing.T) {
	var rendered, raw string
	h := tt.Start(t, tui.Overrides{}, func(h *tt.Harness) {
		rendered = h.MakeRecording("2026-01-01_10-00-00", "capture.mp4", "telemetry.json", "export.mp4")
		raw = h.MakeRecording("2026-01-02_10-00-00", "capture.mp4")
	})
	h.Press(tt.Char('3'))
	h.WaitScreen("2026-01-01_10-00-00  rendered")
	h.WaitScreen("2026-01-02_10-00-00  raw only")

	// Rename: prefilled with the current label, which is none.
	h.Press(tt.Char('n'))
	h.WaitScreen("a label kept after the timestamp")
	h.Type("demo")
	h.Press(tt.Enter)
	h.WaitStatus("renamed to 2026-01-01_10-00-00_demo")
	rendered += "_demo"
	if !tt.Exists(rendered) {
		t.Fatal("the folder was not renamed")
	}

	// Enter opens the rendered video.
	h.Press(tt.Enter)
	h.WaitFor("open", func(*tui.Model) bool { return len(h.Fakes.Snapshot().Opened) == 1 })
	if got := h.Fakes.Snapshot().Opened[0]; got != filepath.Join(rendered, "export.mp4") {
		t.Errorf("opened %s", got)
	}

	// Drop raw, confirmed.
	h.Press(tt.Char('p'))
	h.WaitScreen(" delete raw files ")
	h.WaitScreen("freeing 0.0 MB")
	h.Press(tt.Char('y'))
	h.WaitStatusPrefix("raw files deleted · freed")
	if tt.Exists(filepath.Join(rendered, "capture.mp4")) || !tt.Exists(filepath.Join(rendered, "export.mp4")) {
		t.Fatal("drop raw should delete the capture and keep the export")
	}
	h.Press(tt.Char('p'))
	h.WaitStatus("nothing but the rendered video is left here")

	// The raw-only recording is the only copy, so it cannot be pruned or compressed.
	h.Press(tt.Char('j'))
	h.Press(tt.Char('p'))
	h.WaitStatus("render it first — the capture is the only copy")
	h.Press(tt.Char('c'))
	h.WaitStatus("render first — nothing to compress yet")

	// Delete: anything but y cancels.
	h.Press(tt.Char('d'))
	h.WaitScreen(" delete recording ")
	h.WaitScreen("moves the whole folder to the Trash")
	h.Press(tt.Char('x'))
	h.WaitStatus("cancelled")
	if len(h.Fakes.Snapshot().Trashed) != 0 || !tt.Exists(raw) {
		t.Fatal("a cancelled delete deleted something")
	}
	h.Press(tt.Char('d'), tt.Char('Y'))
	h.WaitStatusPrefix("moved to Trash · ")
	if got := h.Fakes.Snapshot().Trashed; len(got) != 1 || got[0] != raw {
		t.Fatalf("trashed %v", got)
	}
	// The list shrinks and keeps a selection.
	h.WaitGone("2026-01-02_10-00-00")
	h.WaitScreen("❯ 2026-01-01_10-00-00_demo")
	h.Quit()
}

func TestCompressJob(t *testing.T) {
	var dir string
	h := tt.Start(t, tui.Overrides{}, func(h *tt.Harness) {
		dir = h.MakeRecording("2026-01-01_10-00-00", "capture.mp4", "export.mp4")
		h.Fakes.CompressGate = make(chan struct{})
	})
	h.Press(tt.Char('3'), tt.Char('c'))
	h.WaitScreen("target size in MB, e.g. 25")
	h.Type("lots")
	h.Press(tt.Enter)
	h.WaitStatus(`"lots" is not a size in MB — try 25`)

	h.Press(tt.Char('c'))
	h.Type("25")
	h.Press(tt.Enter)
	h.WaitStatus("compressing…")
	h.WaitScreen(" working ")
	h.WaitScreen("compressing")
	close(h.Fakes.CompressGate)
	report := h.WaitReport()
	if !strings.HasPrefix(report, "already under 25 MB — copied as-is") ||
		!strings.HasSuffix(report, "saved to "+filepath.Join(dir, "export-compressed.mp4")) {
		t.Errorf("report %q", report)
	}
	h.Press(tt.Char('x'))
	h.Quit()
}
