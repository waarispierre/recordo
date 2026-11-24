package record_test

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/waarispierre/recordo/internal/domain/config"
	"github.com/waarispierre/recordo/internal/features/record"
	"github.com/waarispierre/recordo/internal/tui"
	tt "github.com/waarispierre/recordo/internal/tui/tuitest"
)

func TestRecordingWithPauseThenRender(t *testing.T) {
	zoom := 25.0
	h := tt.Start(t, tui.Overrides{Zoom: &zoom}, func(h *tt.Harness) {
		h.Fakes.RenderGate = make(chan struct{})
	})

	h.Press(tt.Char('j'), tt.Enter) // the Safari window
	h.WaitScreen(" recording ")
	h.WaitScreen("Safari — Docs (1200x800) · mic: Fake Mic")
	h.WaitScreen("p/space pause · enter/s stop · q stop and quit")

	h.Press(tt.Char('p'))
	h.WaitStatus("paused")
	h.WaitScreen("  paused")
	h.Press(tt.Space)
	h.WaitStatus("recording…")
	h.WaitGone("  paused")

	h.Press(tt.Enter)
	h.WaitStatus("rendering…")
	h.WaitScreen("50/100 frames")
	close(h.Fakes.RenderGate)
	report := h.WaitReport()

	got := h.Fakes.Snapshot()
	for _, want := range []string{
		"⚠ a test warning",
		"10 frames · 20 cursor samples · 2 clicks",
		"1920x1080 · 100 frames · 2.0x realtime",
		"saved to " + filepath.Join(got.Sessions[0], "export.mp4"),
	} {
		if !strings.Contains(report, want) {
			t.Errorf("report lacks %q:\n%s", want, report)
		}
	}
	h.WaitScreen(" done — press any key ")

	if len(got.Targets) != 1 || got.Targets[0] != (record.Target{Kind: record.TargetWindow, WindowID: 42}) {
		t.Errorf("recorded %+v", got.Targets)
	}
	if got.Configs[0].Camera.ZoomPercent != 25 {
		t.Errorf("the --zoom override was not applied: %v", got.Configs[0].Camera.ZoomPercent)
	}
	if len(got.Pauses) != 1 || got.Pauses[0].EndNs <= got.Pauses[0].StartNs {
		t.Errorf("pauses %+v", got.Pauses)
	}
	if len(got.Renders) != 1 || got.Renders[0].ZoomPercent == nil || *got.Renders[0].ZoomPercent != 25 {
		t.Errorf("render options %+v", got.Renders)
	}
	if len(got.Opened) != 1 {
		t.Errorf("opened %v", got.Opened)
	}

	h.Press(tt.Char('x'))
	h.Quit()
}

func TestQuittingMidRecordingFinalisesIt(t *testing.T) {
	h := tt.Start(t, tui.Overrides{}, nil)
	h.Press(tt.Enter)
	h.WaitScreen("Safari — Docs")
	h.Press(tt.Char('q'))
	m := h.Finish()
	farewell := m.FarewellText()
	if !strings.HasPrefix(farewell, "⚠ a test warning\nsaved to ") {
		t.Fatalf("farewell %q", farewell)
	}
	dir := strings.TrimPrefix(farewell, "⚠ a test warning\nsaved to ")
	if !tt.Exists(filepath.Join(dir, "capture.mp4")) {
		t.Fatal("the capture was not finalised")
	}
	if len(h.Fakes.Snapshot().Renders) != 0 {
		t.Fatal("quitting should not render")
	}
}

func TestSecondsLimitStopsTheRecording(t *testing.T) {
	h := tt.Start(t, tui.Overrides{Seconds: 1, NoRender: true}, nil)
	h.Press(tt.Enter)
	report := h.WaitReport()
	if !strings.Contains(report, "10 frames · 20 cursor samples") || !strings.Contains(report, "saved to ") {
		t.Fatalf("report %q", report)
	}
	if got := h.Fakes.Snapshot().Targets[0]; got.Kind != record.TargetDisplay {
		t.Errorf("target %+v", got)
	}
	h.Press(tt.Char('x'))
	// Without a render, the new recording still shows up.
	h.Press(tt.Char('3'))
	h.WaitScreen("raw only")
	h.Quit()
}

func TestAFailedRecordingIsReported(t *testing.T) {
	h := tt.Start(t, tui.Overrides{}, func(h *tt.Harness) {
		h.Fakes.RecordErr = errors.New("no displays found")
	})
	h.Press(tt.Enter)
	h.WaitReport()
	h.WaitScreen("failed: no displays found")
	h.Press(tt.Char('x'))
	h.Quit()
}

func TestPreviewToggle(t *testing.T) {
	h := tt.Start(t, tui.Overrides{}, nil)
	h.Press(tt.Char('w'))
	h.WaitStatus("preview · Fake Cam")
	h.WaitScreen("████") // the flat grey frame renders solid
	h.Press(tt.Char('w'))
	h.WaitStatus("preview off")
	h.WaitGone("████")
	h.WaitFor("the camera to be released", func(*tui.Model) bool {
		cams := h.Fakes.Snapshot().Cams
		return len(cams) == 1 && cams[0].Stopped()
	})
	h.Quit()
}

func TestCountdownWithWebcam(t *testing.T) {
	h := tt.Start(t, tui.Overrides{NoRender: true}, func(h *tt.Harness) {
		for k, v := range map[string]string{"webcam.enabled": "true", "webcam.preview_s": "5"} {
			if err := config.SetValue(h.ConfigPath, k, v); err != nil {
				t.Fatal(err)
			}
		}
	})

	// Escape backs out and, since the preview was not up before, closes it again.
	h.Press(tt.Enter)
	h.WaitScreen("recording in 5")
	h.WaitScreen("check your framing")
	h.WaitScreen("████")
	h.Press(tt.Esc)
	h.WaitStatus("cancelled")
	h.WaitGone("recording in")
	h.WaitFor("the camera to be released", func(*tui.Model) bool {
		cams := h.Fakes.Snapshot().Cams
		return len(cams) == 1 && cams[0].Stopped()
	})
	if len(h.Fakes.Snapshot().Targets) != 0 {
		t.Fatal("a cancelled countdown recorded")
	}

	// With the preview up first, enter during the countdown starts at once; the framing
	// camera is released before the recording opens its own, and comes back afterwards.
	h.Press(tt.Char('w'))
	h.WaitStatus("preview · Fake Cam")
	h.Press(tt.Enter)
	h.WaitScreen("recording in")
	h.Press(tt.Enter)
	h.WaitScreen(" recording ")
	h.Press(tt.Char('s'))
	h.WaitReport()
	got := h.Fakes.Snapshot()
	if len(got.CamsReleased) != 1 || !got.CamsReleased[0] {
		t.Fatal("the framing camera was still open when recording started")
	}
	h.WaitStatus("preview · Fake Cam")
	if n := len(h.Fakes.Snapshot().Cams); n != 3 {
		t.Errorf("opened %d cameras, want 3", n)
	}
	h.Press(tt.Char('x'))
	h.Quit()
}
