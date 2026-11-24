// Command recordo records demos with a cursor-following camera.
//
// main only assembles the root command; each feature lives in its own slice under
// internal/features.
package main

import (
	"context"
	"os"

	"charm.land/fang/v2"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/waarispierre/recordo/internal/cli"
	"github.com/waarispierre/recordo/internal/features/compress"
	"github.com/waarispierre/recordo/internal/features/doctor"
	"github.com/waarispierre/recordo/internal/features/record"
	"github.com/waarispierre/recordo/internal/features/recordings"
	"github.com/waarispierre/recordo/internal/features/render"
	"github.com/waarispierre/recordo/internal/features/settings"
	"github.com/waarispierre/recordo/internal/platform/macos"
)

func rootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:   "recordo",
		Short: "Screen recordings with a cursor-following camera",
		Long: "Records an app window or the whole display, then re-renders it with a smooth " +
			"auto-zoom that follows your cursor, rounded corners, a drop shadow and a styled background.",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// A bare `recordo` opens the full-screen app, like lazygit. Any targeting flag
			// means the one-shot path, and a non-terminal always does.
			if !record.AnyTarget() && term.IsTerminal(int(os.Stdin.Fd())) {
				return runTUI(cmd)
			}
			return record.RunCLI(cmd)
		},
	}
	cli.Register(root)
	root.AddCommand(record.Command(), render.Command(), compress.Command(), settings.Command())
	root.AddCommand(doctor.Commands()...)
	root.AddCommand(recordings.Commands()...)
	return root
}

func main() {
	// The main thread belongs to macOS's run loop, which the webcam needs; the program
	// runs on a goroutine. See macos.Main.
	macos.Main(func() int {
		// No signal option: Ctrl-C while recording must end the recording cleanly, which
		// the record command handles itself.
		if err := fang.Execute(context.Background(), rootCommand(), fang.WithVersion(cli.Version)); err != nil {
			return 1
		}
		return 0
	})
}
