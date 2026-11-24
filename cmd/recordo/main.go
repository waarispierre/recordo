// Command recordo records demos with a cursor-following camera.
package main

import (
	"context"
	"fmt"
	"os"

	"charm.land/fang/v2"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/waarispierre/recordo/internal/domain/config"
	"github.com/waarispierre/recordo/internal/domain/session"
	"github.com/waarispierre/recordo/internal/features/record"
	"github.com/waarispierre/recordo/internal/platform/macos"
)

// version is set at build time with -ldflags "-X main.version=…".
var version = "dev"

// flags are the global options, shared by every subcommand as in the Rust CLI.
type flags struct {
	app      string
	window   uint32
	display  bool
	seconds  uint
	zoom     float64
	noRender bool
	noOpen   bool
	mic      bool
	noMic    bool
	webcam   bool
	noWebcam bool
}

var opt flags

// zoomOverride is --zoom when it was given.
func zoomOverride(cmd *cobra.Command) *float64 {
	if cmd.Flags().Changed("zoom") {
		z := opt.zoom
		return &z
	}
	return nil
}

// override folds an opposing --x / --no-x pair into one, nil when neither was given.
func override(on, off bool) *bool {
	switch {
	case on:
		t := true
		return &t
	case off:
		f := false
		return &f
	}
	return nil
}

func target() record.Target {
	switch {
	case opt.display:
		return record.Target{Kind: record.TargetDisplay}
	case opt.window != 0:
		return record.Target{Kind: record.TargetWindow, WindowID: opt.window}
	case opt.app != "":
		return record.Target{Kind: record.TargetApp, App: opt.app}
	}
	return record.Target{Kind: record.TargetAsk}
}

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
			if opt.app == "" && opt.window == 0 && !opt.display && term.IsTerminal(int(os.Stdin.Fd())) {
				return runTUI(cmd)
			}
			return cmdRecord(cmd)
		},
	}
	p := root.PersistentFlags()
	p.StringVarP(&opt.app, "app", "a", "", "record this app (matched on name, largest visible window wins)")
	p.Uint32VarP(&opt.window, "window", "w", 0, "record this window id (see `recordo windows`)")
	p.BoolVarP(&opt.display, "display", "d", false, "record the whole display")
	p.UintVarP(&opt.seconds, "seconds", "s", 0, "stop after this many seconds instead of waiting for Enter")
	p.Float64VarP(&opt.zoom, "zoom", "z", 0, "zoom percentage for this run only, e.g. 30 (overrides config)")
	p.BoolVar(&opt.noRender, "no-render", false, "record but do not render")
	p.BoolVar(&opt.noOpen, "no-open", false, "do not open the finished video")
	p.BoolVar(&opt.mic, "mic", false, "record voice over for this run only (overrides config)")
	p.BoolVar(&opt.noMic, "no-mic", false, "skip voice over for this run only (overrides config)")
	p.BoolVar(&opt.webcam, "webcam", false, "show the webcam overlay for this run only (overrides config)")
	p.BoolVar(&opt.noWebcam, "no-webcam", false, "skip the webcam overlay for this run only (overrides config)")
	root.MarkFlagsMutuallyExclusive("mic", "no-mic")
	root.MarkFlagsMutuallyExclusive("webcam", "no-webcam")

	root.AddCommand(
		&cobra.Command{
			Use: "record", Short: "Record and render (the default)", Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error { return cmdRecord(cmd) },
		},
		renderCommand(),
		compressCommand(),
		&cobra.Command{
			Use: "windows", Short: "List windows that can be recorded", Args: cobra.NoArgs,
			RunE: func(*cobra.Command, []string) error { return cmdWindows() },
		},
		&cobra.Command{
			Use: "devices", Short: "List cameras and microphones, and the names the config accepts", Args: cobra.NoArgs,
			RunE: func(*cobra.Command, []string) error { return cmdDevices() },
		},
		doctorCommand(),
		configCommand(),
		&cobra.Command{
			Use: "list", Short: "List past recordings", Args: cobra.NoArgs,
			RunE: func(*cobra.Command, []string) error { return cmdList() },
		},
		pruneCommand(),
	)
	return root
}

func main() {
	// The main thread belongs to macOS's run loop, which the webcam needs; the program
	// runs on a goroutine. See macos.Main.
	macos.Main(func() int {
		// No signal option: Ctrl-C while recording must end the recording cleanly, which
		// the record command handles itself.
		if err := fang.Execute(context.Background(), rootCommand(), fang.WithVersion(version)); err != nil {
			return 1
		}
		return 0
	})
}

// loadConfig reads the config file, writing a commented default on first run.
func loadConfig() (config.Config, string, error) {
	path, err := configPath()
	if err != nil {
		return config.Config{}, "", err
	}
	cfg, err := config.LoadOrCreate(path)
	return cfg, path, err
}

func configPath() (string, error) {
	p, err := session.ConfigPath()
	if err != nil {
		return "", fmt.Errorf("locate config: %w", err)
	}
	return p, nil
}
