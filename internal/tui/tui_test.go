package tui_test

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/waarispierre/recordo/internal/tui"
	tt "github.com/waarispierre/recordo/internal/tui/tuitest"
)

func TestTabSwitching(t *testing.T) {
	h := tt.Start(t, tui.Overrides{}, nil)
	h.WaitScreen("what to record")
	h.WaitScreen("Safari — Docs (1200x800)")
	// The wordmark's tagline, in the empty space under the short window list.
	h.WaitScreen("screen recordings with a cursor-following camera")

	steps := []struct {
		key  tea.KeyPressMsg
		text string
	}{
		{tt.Tab, "what it does"},
		{tt.Char('l'), "no recordings yet — press 1 to make one"},
		{tt.Tab, "what to record"},
		{tt.BackTab, "enter open · r render"},
		{tt.Char('h'), "what it does"},
		{tt.Char('3'), "enter open · r render"},
		{tt.Char('1'), "enter record · w preview · tab switch · q quit"},
		{tt.Char('2'), "enter edit · tab switch · q quit"},
	}
	for _, s := range steps {
		h.Press(s.key)
		h.WaitScreen(s.text)
	}

	// j/k wrap around the list.
	h.Press(tt.Char('1'), tt.Char('k'))
	h.WaitScreen("❯ Safari — Docs")
	h.Press(tt.Char('j'))
	h.WaitScreen("❯ entire display")

	h.Quit()
}

func TestTheReportIsDismissedByAnyKey(t *testing.T) {
	h := tt.Start(t, tui.Overrides{NoRender: true}, nil)
	h.Press(tt.Enter, tt.Char('s'))
	h.WaitReport()
	h.WaitScreen(" done — press any key ")
	h.WaitStatus("press any key to dismiss")
	h.Press(tt.Char('x'))
	h.WaitStatus("j/k move · enter select · tab switch · q quit")
	h.WaitGone(" done — press any key ")
	h.Quit()
}

func TestQuittingDuringAJobWaitsForIt(t *testing.T) {
	h := tt.Start(t, tui.Overrides{}, func(h *tt.Harness) {
		h.MakeRecording("2026-01-01_10-00-00", "capture.mp4")
		h.Fakes.RenderGate = make(chan struct{})
	})
	h.Press(tt.Char('3'), tt.Char('r'))
	h.WaitScreen(" working ")
	h.WaitScreen("50/100 frames")
	h.Press(tt.Char('q'))
	h.WaitStatus("finishing up…")
	// Still running: the program only ends once the render does.
	h.WaitScreen("50/100 frames")
	close(h.Fakes.RenderGate)
	m := h.Finish()
	if report, up := m.Report(); up || m.FarewellText() != "" {
		t.Fatalf("quitting during a job should leave quietly: %q %q", report, m.FarewellText())
	}
}

func TestAFinishedJobRefreshesTheRecordings(t *testing.T) {
	h := tt.Start(t, tui.Overrides{}, func(h *tt.Harness) {
		h.MakeRecording("2026-01-01_10-00-00", "capture.mp4")
	})
	h.Press(tt.Char('3'))
	h.WaitScreen("2026-01-01_10-00-00  raw only")
	h.Press(tt.Char('r'))
	report := h.WaitReport()
	if !strings.HasPrefix(report, "1920x1080 · 100 frames · 2.0x realtime\nsaved to ") {
		t.Errorf("report %q", report)
	}
	h.Press(tt.Char('x'))
	h.WaitScreen("2026-01-01_10-00-00  rendered")
	h.Quit()
}
