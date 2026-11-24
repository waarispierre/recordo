package main

import (
	"github.com/spf13/cobra"
	"github.com/waarispierre/recordo/internal/cli"

	"github.com/waarispierre/recordo/internal/tui"
)

// runTUI opens the full-screen app, handing over the flags that would otherwise be lost:
// a bare `recordo --zoom 30` still opens it.
func runTUI(cmd *cobra.Command) error {
	return tui.Run(tui.Overrides{
		Zoom:     cli.ZoomOverride(cmd),
		Mic:      cli.Override(cli.Opt.Mic, cli.Opt.NoMic),
		Webcam:   cli.Override(cli.Opt.Webcam, cli.Opt.NoWebcam),
		Seconds:  cli.Opt.Seconds,
		NoRender: cli.Opt.NoRender,
		NoOpen:   cli.Opt.NoOpen,
	})
}
