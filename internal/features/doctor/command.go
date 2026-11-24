// Package doctor is the doctor slice: check permissions and tooling, and list what can
// be recorded with — windows, cameras, microphones.
package doctor

import (
	"fmt"
	"github.com/waarispierre/recordo/internal/cli"

	"github.com/spf13/cobra"

	"github.com/waarispierre/recordo/internal/domain/config"
	"github.com/waarispierre/recordo/internal/domain/session"
	"github.com/waarispierre/recordo/internal/features/record"
	"github.com/waarispierre/recordo/internal/platform/ffmpeg"
	"github.com/waarispierre/recordo/internal/platform/macos"
)

func cmdWindows() error {
	content, err := macos.ShareableContent()
	if err != nil {
		return err
	}
	windows := record.Recordable(content)
	if len(windows) == 0 {
		cli.Say("no windows found")
		return nil
	}
	fmt.Println()
	cli.Say("%9s  %s", cli.Bold.Render("ID"), cli.Bold.Render("WINDOW"))
	for _, w := range windows {
		cli.Say("%s  %s", cli.Cyan.Render(fmt.Sprintf("%9d", w.ID)), record.Label(w))
	}
	fmt.Println()
	cli.Say("%s", cli.Dim.Render("recordo -w <ID>    or    recordo -a <app name>"))
	fmt.Println()
	return nil
}

func cmdDevices() error {
	list := func(title string, ds []macos.Device) {
		fmt.Println()
		cli.Say("%s", cli.Bold.Render(title))
		if len(ds) == 0 {
			cli.Say("none found")
		}
		for _, d := range ds {
			cli.Say("%s %s", cli.Dot, d.Name)
		}
	}
	list("microphones", macos.Microphones())
	list("cameras", macos.Cameras())
	fmt.Println()
	cli.Say("%s", cli.Dim.Render(`recordo config set audio.device "<name>"    or    webcam.device`))
	fmt.Println()
	return nil
}

func check(name string, ok bool, hint string) {
	if ok {
		cli.Say("%s %-18s", cli.Tick, name)
	} else {
		cli.Say("%s %-18s %s", cli.Fail, name, cli.Dim.Render(hint))
	}
}

func doctorCmd() *cobra.Command {
	var verbose bool
	cmd := &cobra.Command{
		Use: "doctor", Short: "Check permissions and tooling", Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error { return cmdDoctor(verbose) },
	}
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "also report what page bounds each open browser exposes")
	return cmd
}

func cmdDoctor(verbose bool) error {
	fmt.Println()
	cli.Say("%s", cli.Bold.Render("recordo doctor"))
	fmt.Println()
	_, err := macos.ShareableContent()
	check("Screen Recording", err == nil, "required — grant it to your terminal, then restart it")
	check("Accessibility", macos.AccessibilityTrusted(),
		"optional — without it, browser pages are cropped by a fixed guess")

	// Read-only: these ask what was already decided and never prompt. A doctor command
	// that pops a permission dialog is one nobody would run twice.
	for _, p := range []struct {
		name   string
		access macos.Access
	}{{"Microphone", macos.MicrophoneAccess()}, {"Camera", macos.CameraAccess()}} {
		mark := cli.Dim.Render("·")
		if p.access == macos.Granted {
			mark = cli.Tick
		}
		cli.Say("%s %-18s %s", mark, p.name, cli.Dim.Render(p.access.Explain()))
	}

	// Report the absolute binary that will actually run, not merely "something named
	// ffmpeg is on $PATH".
	for _, name := range []string{"ffmpeg", "ffprobe"} {
		if p, ok := ffmpeg.Find(name); ok {
			cli.Say("%s %-18s %s", cli.Tick, name, cli.Dim.Render(p))
		} else {
			check(name, false, "required to render — brew install ffmpeg")
		}
	}

	cfg, err := session.ConfigPath()
	if err != nil {
		return err
	}
	videos, err := session.RecordingsDir()
	if err != nil {
		return err
	}
	fmt.Println()
	cli.Say("%s %s", cli.Dim.Render("config"), cfg)
	cli.Say("%s %s", cli.Dim.Render("videos"), videos)
	if verbose {
		return browserReport()
	}
	cli.Say("%s", cli.Dim.Render("--verbose adds per-browser detail"))
	fmt.Println()
	return nil
}

// browserReport shows what page bounds each open browser reports, which is what decides
// whether a recording is cropped exactly or by the fixed fallback.
func browserReport() error {
	fmt.Println()
	cli.Say("%s", cli.Bold.Render("browsers"))
	content, err := macos.ShareableContent()
	if err != nil {
		return err
	}
	found := false
	for _, w := range record.Recordable(content) {
		if !config.IsBrowser(w.BundleID) {
			continue
		}
		found = true
		if r, ok := macos.WebContentRect(w.PID, w.Frame); ok {
			cli.Say("%s %-16spage %.0fx%.0f, chrome %.0fpt %s", cli.Dot, w.AppName, r.W, r.H, r.Y-w.Frame.Y, cli.Green.Render("exact"))
		} else {
			cli.Say("%s %-16s%s", cli.Dot, w.AppName, cli.Yellow.Render("page bounds unavailable — will use the fixed crop"))
		}
	}
	if !found {
		cli.Say("%s no browser windows open", cli.Dot)
	}
	fmt.Println()
	return nil
}

// Commands are `recordo doctor`, `windows` and `devices`.
func Commands() []*cobra.Command {
	return []*cobra.Command{
		doctorCmd(),
		{
			Use: "windows", Short: "List windows that can be recorded", Args: cobra.NoArgs,
			RunE: func(*cobra.Command, []string) error { return cmdWindows() },
		},
		{
			Use: "devices", Short: "List cameras and microphones, and the names the config accepts", Args: cobra.NoArgs,
			RunE: func(*cobra.Command, []string) error { return cmdDevices() },
		},
	}
}
