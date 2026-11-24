package tui

import (
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"sync/atomic"

	tea "charm.land/bubbletea/v2"

	"github.com/waarispierre/recordo/internal/domain/session"
	"github.com/waarispierre/recordo/internal/features/compress"
	"github.com/waarispierre/recordo/internal/features/record"
	"github.com/waarispierre/recordo/internal/features/render"
)

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

// startRenderOf renders a recording that was just made, then opens it. Unlike the Rust
// version, a one-off --webcam/--no-webcam carries into this render, as it does on the CLI
// path.
func (m *Model) startRenderOf(s *session.Session, health record.Health, warnings []string) tea.Cmd {
	run, opts, noOpen, open := m.deps.render, m.renderOptions(), m.overrides.NoOpen, m.deps.open
	return m.startJob(jobRender, "rendering…", func(progress func(int, int)) (string, error) {
		out := s.Export()
		opts.Progress = progress
		rep, err := run(s.Capture(), out, opts)
		if err != nil {
			return "", err
		}
		if !noOpen {
			open(out)
		}
		lines := append(append([]string(nil), warnings...), summarizeRecord(health, rep, out))
		return strings.Join(lines, "\n"), nil
	})
}

// startRender re-renders a recording from the Recordings tab.
func (m *Model) startRender(dir string) tea.Cmd {
	run, opts := m.deps.render, m.renderOptions()
	return m.startJob(jobRender, "rendering…", func(progress func(int, int)) (string, error) {
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
		return summarizeRender(rep, out), nil
	})
}

func (m *Model) renderOptions() render.Options {
	return render.Options{ZoomPercent: m.overrides.Zoom, Webcam: m.overrides.Webcam}
}

func (m *Model) startCompress(dir string, maxMB float64) tea.Cmd {
	compress := m.deps.compress
	return m.startJob(jobCompress, "compressing…", func(func(int, int)) (string, error) {
		src := filepath.Join(dir, "export.mp4")
		dst := filepath.Join(dir, "export-compressed.mp4")
		rep, err := compress(src, dst, uint64(maxMB*1_000_000))
		if err != nil {
			return "", err
		}
		return summarizeCompress(rep, maxMB, dst), nil
	})
}

// summarizeRecord is what the CLI's health report and render summary print, condensed
// into the popup shown when a job finishes — that detail must not silently vanish now
// that nothing prints to a plain terminal.
func summarizeRecord(h record.Health, rep render.Report, out string) string {
	lines := healthLines(h)
	lines = append(lines, renderLine(rep))
	if rep.CropSource == render.CropFixedGuess {
		lines = append(lines, fmt.Sprintf("%s page bounds unavailable — cropped by a fixed guess, so tabs may show", rep.AppName))
	}
	lines = append(lines, "saved to "+out)
	return strings.Join(lines, "\n")
}

// healthLines is the cursor/click sanity check the CLI prints after a recording.
func healthLines(h record.Health) []string {
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

func summarizeRender(rep render.Report, out string) string {
	return renderLine(rep) + "\nsaved to " + out
}

func renderLine(rep render.Report) string {
	extra := ""
	if rep.Audio {
		extra += " · voice over"
	}
	if rep.Webcam {
		extra += " · webcam"
	}
	return fmt.Sprintf("%dx%d · %d frames · %.1fx realtime%s", rep.OutW, rep.OutH, rep.Frames, rep.RealtimeRatio(), extra)
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
