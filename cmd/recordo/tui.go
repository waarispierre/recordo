package main

import (
	"github.com/spf13/cobra"

	"github.com/waarispierre/recordo/internal/tui"
)

// runTUI opens the full-screen app, handing over the flags that would otherwise be lost:
// a bare `recordo --zoom 30` still opens it.
func runTUI(cmd *cobra.Command) error {
	return tui.Run(tui.Overrides{
		Zoom:     zoomOverride(cmd),
		Mic:      override(opt.mic, opt.noMic),
		Webcam:   override(opt.webcam, opt.noWebcam),
		Seconds:  opt.seconds,
		NoRender: opt.noRender,
		NoOpen:   opt.noOpen,
	})
}
