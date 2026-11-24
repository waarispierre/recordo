package main

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/spf13/cobra"

	"github.com/waarispierre/recordo/internal/domain/session"
	"github.com/waarispierre/recordo/internal/features/compress"
	"github.com/waarispierre/recordo/internal/features/render"
)

func renderCommand() *cobra.Command {
	var out string
	cmd := &cobra.Command{
		Use:   "render [recording-folder]",
		Short: "Render a recording again, picking up config changes",
		Long:  "Render a recording again, picking up config changes. Defaults to the most recent recording.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var s *session.Session
			var err error
			if len(args) == 1 {
				s, err = session.Open(args[0])
			} else {
				s, err = session.Latest()
			}
			if err != nil {
				return err
			}
			dst := s.Export()
			if out != "" {
				if dst, err = filepath.Abs(out); err != nil {
					return err
				}
			}
			fmt.Println()
			say("%s %s", green.Bold(true).Render("rendering"), dim.Render(s.Dir))
			return renderSession(s, dst, zoomOverride(cmd), override(opt.webcam, opt.noWebcam))
		},
	}
	cmd.Flags().StringVarP(&out, "output", "o", "", "write here instead of export.mp4 in the recording")
	return cmd
}

// renderSession renders with a progress line, prints the summary, and opens the result.
func renderSession(s *session.Session, dst string, zoom *float64, webcam *bool) error {
	start := time.Now()
	rep, err := render.Run(s.Capture(), dst, render.Options{
		ZoomPercent: zoom,
		Webcam:      webcam,
		Progress: func(done, total int) {
			if done%15 == 0 || done == total {
				pct := 100 * done / max(total, 1)
				filled := pct / 5
				bar := cyan.Render(repeat("━", filled)) + dim.Render(repeat("━", 20-filled))
				lipgloss.Printf("\r  %s %3d%%  %s", bar, pct, dim.Render(time.Since(start).Truncate(time.Second).String()))
			}
		},
	})
	fmt.Print("\r\033[K")
	if err != nil {
		return err
	}
	extra := ""
	if rep.Audio {
		extra += " · voice over"
	}
	if rep.Webcam {
		extra += " · webcam"
	}
	say("%s %dx%d · %d frames · %.1fx realtime%s", dot, rep.OutW, rep.OutH, rep.Frames, rep.RealtimeRatio(), extra)
	if rep.CropSource == render.CropFixedGuess {
		say("%s %s page bounds unavailable — cropped by a fixed guess, so tabs may show", warn, rep.AppName)
	}
	fmt.Println()
	say("🏁 %s", bold.Render(dst))
	openFile(dst)
	return nil
}

func repeat(s string, n int) string {
	out := ""
	for range max(n, 0) {
		out += s
	}
	return out
}

func compressCommand() *cobra.Command {
	var maxMB float64
	cmd := &cobra.Command{
		Use:   "compress [recording-folder]",
		Short: "Shrink a rendered export to fit under a size limit, e.g. for PR/CI uploads",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if math.IsNaN(maxMB) || math.IsInf(maxMB, 0) || maxMB <= 0 {
				return errors.New("--max-mb must be greater than 0")
			}
			path := ""
			if len(args) == 1 {
				path = args[0]
			}
			// Deliberately not session.Open/Latest, which require a capture.mp4: `recordo
			// prune` drops that raw capture on purpose while keeping the export, and
			// compressing an already-rendered video for sharing is exactly the situation
			// that leaves you in.
			dir, err := compressDir(path)
			if err != nil {
				return err
			}
			src, dst := filepath.Join(dir, "export.mp4"), filepath.Join(dir, "export-compressed.mp4")
			fmt.Println()
			say("%s %s", green.Bold(true).Render("compressing"), dim.Render(dir))
			rep, err := compress.CompressToSize(src, dst, uint64(maxMB*1_000_000))
			if err != nil {
				return err
			}
			mb := func(b uint64) float64 { return float64(b) / 1_000_000 }
			if rep.Reencoded {
				say("%s %.1f MB → %.1f MB · %.1f Mbps video", dot, mb(rep.InputBytes), mb(rep.OutputBytes),
					float64(rep.VideoBitrateBps)/1_000_000)
			} else {
				say("%s already under %.0f MB — copied as-is", dot, maxMB)
			}
			if !rep.Fits() {
				say("%s still %.1f MB, over the %.0f MB target — try a lower --max-mb", warn, mb(rep.OutputBytes), maxMB)
			}
			fmt.Println()
			say("🏁 %s", bold.Render(dst))
			return nil
		},
	}
	cmd.Flags().Float64Var(&maxMB, "max-mb", 0, "target size in megabytes (1,000,000 bytes), e.g. 25 for Azure DevOps, 10 for GitHub")
	_ = cmd.MarkFlagRequired("max-mb")
	return cmd
}

// compressDir is the given folder, or the most recently rendered one. It only requires
// an export.mp4.
func compressDir(path string) (string, error) {
	if path != "" {
		if !exists(filepath.Join(path, "export.mp4")) {
			return "", fmt.Errorf("%s does not contain an export.mp4 — run `recordo render` first", path)
		}
		return path, nil
	}
	root, err := session.RecordingsDir()
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", root, err)
	}
	for i := len(entries) - 1; i >= 0; i-- {
		dir := filepath.Join(root, entries[i].Name())
		if exists(filepath.Join(dir, "export.mp4")) {
			return dir, nil
		}
	}
	return "", errors.New("no rendered recordings yet — run `recordo render` first")
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
