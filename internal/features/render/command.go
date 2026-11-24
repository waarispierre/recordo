package render

import (
	"fmt"
	"github.com/waarispierre/recordo/internal/cli"
	"path/filepath"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/spf13/cobra"

	"github.com/waarispierre/recordo/internal/domain/session"
)

func Command() *cobra.Command {
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
			cli.Say("%s %s", cli.Green.Bold(true).Render("rendering"), cli.Dim.Render(s.Dir))
			return Present(s, dst, cli.ZoomOverride(cmd), cli.Override(cli.Opt.Webcam, cli.Opt.NoWebcam))
		},
	}
	cmd.Flags().StringVarP(&out, "output", "o", "", "write here instead of export.mp4 in the recording")
	return cmd
}

// Present renders a recording with a progress line, prints the summary, and opens the
// result. The record slice calls it after recording.
func Present(s *session.Session, dst string, zoom *float64, webcam *bool) error {
	start := time.Now()
	rep, err := Run(s.Capture(), dst, Options{
		ZoomPercent: zoom,
		Webcam:      webcam,
		Progress: func(done, total int) {
			if done%15 == 0 || done == total {
				pct := 100 * done / max(total, 1)
				filled := pct / 5
				bar := cli.Cyan.Render(repeat("━", filled)) + cli.Dim.Render(repeat("━", 20-filled))
				lipgloss.Printf("\r  %s %3d%%  %s", bar, pct, cli.Dim.Render(time.Since(start).Truncate(time.Second).String()))
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
	cli.Say("%s %dx%d · %d frames · %.1fx realtime%s", cli.Dot, rep.OutW, rep.OutH, rep.Frames, rep.RealtimeRatio(), extra)
	if rep.CropSource == CropFixedGuess {
		cli.Say("%s %s page bounds unavailable — cropped by a fixed guess, so tabs may show", cli.Warn, rep.AppName)
	}
	fmt.Println()
	cli.Say("🏁 %s", cli.Bold.Render(dst))
	cli.OpenFile(dst)
	return nil
}

func repeat(s string, n int) string {
	out := ""
	for range max(n, 0) {
		out += s
	}
	return out
}
