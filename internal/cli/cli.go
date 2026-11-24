// Package cli is the shared kit for the slices' commands: the global flags, terminal
// styles, and loading the config. It holds no feature logic.
package cli

import (
	"fmt"
	"os"
	"os/exec"
	"runtime/debug"

	"charm.land/lipgloss/v2"
	"github.com/spf13/cobra"

	"github.com/waarispierre/recordo/internal/domain/config"
	"github.com/waarispierre/recordo/internal/domain/session"
)

// Version is set at build time with -ldflags "-X github.com/waarispierre/recordo/internal/cli.Version=…",
// which install.sh does. A `go install …@version` build has no such flag, so it falls back
// to the module version Go embeds in every binary.
var Version = "dev"

func init() {
	if Version != "dev" {
		return
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		Version = info.Main.Version
	}
}

// Flags are the global options, shared by every subcommand as in the original CLI.
type Flags struct {
	App      string
	Window   uint32
	Display  bool
	Seconds  uint
	Zoom     float64
	NoRender bool
	NoOpen   bool
	Mic      bool
	NoMic    bool
	Webcam   bool
	NoWebcam bool
}

// Opt holds the parsed global flags.
var Opt Flags

// Register adds the global flags to the root command.
func Register(root *cobra.Command) {
	p := root.PersistentFlags()
	p.StringVarP(&Opt.App, "app", "a", "", "record this app (matched on name, largest visible window wins)")
	p.Uint32VarP(&Opt.Window, "window", "w", 0, "record this window id (see `recordo windows`)")
	p.BoolVarP(&Opt.Display, "display", "d", false, "record the whole display")
	p.UintVarP(&Opt.Seconds, "seconds", "s", 0, "stop after this many seconds instead of waiting for Enter")
	p.Float64VarP(&Opt.Zoom, "zoom", "z", 0, "zoom percentage for this run only, e.g. 30 (overrides config)")
	p.BoolVar(&Opt.NoRender, "no-render", false, "record but do not render")
	p.BoolVar(&Opt.NoOpen, "no-open", false, "do not open the finished video")
	p.BoolVar(&Opt.Mic, "mic", false, "record voice over for this run only (overrides config)")
	p.BoolVar(&Opt.NoMic, "no-mic", false, "skip voice over for this run only (overrides config)")
	p.BoolVar(&Opt.Webcam, "webcam", false, "show the webcam overlay for this run only (overrides config)")
	p.BoolVar(&Opt.NoWebcam, "no-webcam", false, "skip the webcam overlay for this run only (overrides config)")
	root.MarkFlagsMutuallyExclusive("mic", "no-mic")
	root.MarkFlagsMutuallyExclusive("webcam", "no-webcam")
}

// ZoomOverride is --zoom when it was given.
func ZoomOverride(cmd *cobra.Command) *float64 {
	if cmd.Flags().Changed("zoom") {
		z := Opt.Zoom
		return &z
	}
	return nil
}

// Override folds an opposing --x / --no-x pair into one, nil when neither was given.
func Override(on, off bool) *bool {
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

// Terminal styles. Printing goes through lipgloss.Printf/Println, which drop colour when
// output is piped and downsample it for terminals that cannot show it.
var (
	Bold   = lipgloss.NewStyle().Bold(true)
	Dim    = lipgloss.NewStyle().Foreground(lipgloss.BrightBlack)
	Cyan   = lipgloss.NewStyle().Foreground(lipgloss.Cyan)
	Green  = lipgloss.NewStyle().Foreground(lipgloss.Green)
	Yellow = lipgloss.NewStyle().Foreground(lipgloss.Yellow)
	Red    = lipgloss.NewStyle().Foreground(lipgloss.Red)

	Tick = Green.Render("✓")
	Warn = Yellow.Render("!")
	Fail = Red.Render("✗")
	Dot  = Dim.Render("·")
)

// Say prints one indented line.
func Say(format string, a ...any) { lipgloss.Printf("  "+format+"\n", a...) }

// Swatch is a block in the setting's own colour, so RGB triples read at a glance.
func Swatch(value string) string {
	if r, g, b, ok := config.ParseRGB(value); ok {
		c := lipgloss.Color(fmt.Sprintf("#%02x%02x%02x", r, g, b))
		return lipgloss.NewStyle().Foreground(c).Render("██") + " "
	}
	return ""
}

// LoadConfig reads the config file, writing a commented default on first run.
func LoadConfig() (config.Config, string, error) {
	path, err := ConfigPath()
	if err != nil {
		return config.Config{}, "", err
	}
	cfg, err := config.LoadOrCreate(path)
	return cfg, path, err
}

// ConfigPath is where the config file lives.
func ConfigPath() (string, error) {
	p, err := session.ConfigPath()
	if err != nil {
		return "", fmt.Errorf("locate config: %w", err)
	}
	return p, nil
}

// Exists reports whether a path exists.
func Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// OpenFile shows a finished video unless --no-open. /usr/bin/open launches a separate app
// and returns at once.
func OpenFile(path string) {
	if !Opt.NoOpen {
		_ = exec.Command("/usr/bin/open", path).Run()
	}
}
