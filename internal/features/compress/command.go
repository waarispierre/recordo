package compress

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/waarispierre/recordo/internal/cli"
	"github.com/waarispierre/recordo/internal/domain/session"
)

// Command is `recordo compress`.
func Command() *cobra.Command {
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
			cli.Say("%s %s", cli.Green.Bold(true).Render("compressing"), cli.Dim.Render(dir))
			rep, err := CompressToSize(src, dst, uint64(maxMB*1_000_000))
			if err != nil {
				return err
			}
			mb := func(b uint64) float64 { return float64(b) / 1_000_000 }
			if rep.Reencoded {
				cli.Say("%s %.1f MB → %.1f MB · %.1f Mbps video", cli.Dot, mb(rep.InputBytes), mb(rep.OutputBytes),
					float64(rep.VideoBitrateBps)/1_000_000)
			} else {
				cli.Say("%s already under %.0f MB — copied as-is", cli.Dot, maxMB)
			}
			if !rep.Fits() {
				cli.Say("%s still %.1f MB, over the %.0f MB target — try a lower --max-mb", cli.Warn, mb(rep.OutputBytes), maxMB)
			}
			fmt.Println()
			cli.Say("🏁 %s", cli.Bold.Render(dst))
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
		if !cli.Exists(filepath.Join(path, "export.mp4")) {
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
		if cli.Exists(filepath.Join(dir, "export.mp4")) {
			return dir, nil
		}
	}
	return "", errors.New("no rendered recordings yet — run `recordo render` first")
}
