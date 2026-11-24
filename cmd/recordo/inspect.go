package main

import (
	"fmt"

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
		say("no windows found")
		return nil
	}
	fmt.Println()
	say("%9s  %s", bold.Render("ID"), bold.Render("WINDOW"))
	for _, w := range windows {
		say("%s  %s", cyan.Render(fmt.Sprintf("%9d", w.ID)), record.Label(w))
	}
	fmt.Println()
	say("%s", dim.Render("recordo -w <ID>    or    recordo -a <app name>"))
	fmt.Println()
	return nil
}

func cmdDevices() error {
	list := func(title string, ds []macos.Device) {
		fmt.Println()
		say("%s", bold.Render(title))
		if len(ds) == 0 {
			say("none found")
		}
		for _, d := range ds {
			say("%s %s", dot, d.Name)
		}
	}
	list("microphones", macos.Microphones())
	list("cameras", macos.Cameras())
	fmt.Println()
	say("%s", dim.Render(`recordo config set audio.device "<name>"    or    webcam.device`))
	fmt.Println()
	return nil
}

func check(name string, ok bool, hint string) {
	if ok {
		say("%s %-18s", tick, name)
	} else {
		say("%s %-18s %s", fail, name, dim.Render(hint))
	}
}

func doctorCommand() *cobra.Command {
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
	say("%s", bold.Render("recordo doctor"))
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
		mark := dim.Render("·")
		if p.access == macos.Granted {
			mark = tick
		}
		say("%s %-18s %s", mark, p.name, dim.Render(p.access.Explain()))
	}

	// Report the absolute binary that will actually run, not merely "something named
	// ffmpeg is on $PATH".
	for _, name := range []string{"ffmpeg", "ffprobe"} {
		if p, ok := ffmpeg.Find(name); ok {
			say("%s %-18s %s", tick, name, dim.Render(p))
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
	say("%s %s", dim.Render("config"), cfg)
	say("%s %s", dim.Render("videos"), videos)
	if verbose {
		return browserReport()
	}
	say("%s", dim.Render("--verbose adds per-browser detail"))
	fmt.Println()
	return nil
}

// browserReport shows what page bounds each open browser reports, which is what decides
// whether a recording is cropped exactly or by the fixed fallback.
func browserReport() error {
	fmt.Println()
	say("%s", bold.Render("browsers"))
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
			say("%s %-16spage %.0fx%.0f, chrome %.0fpt %s", dot, w.AppName, r.W, r.H, r.Y-w.Frame.Y, green.Render("exact"))
		} else {
			say("%s %-16s%s", dot, w.AppName, yellow.Render("page bounds unavailable — will use the fixed crop"))
		}
	}
	if !found {
		say("%s no browser windows open", dot)
	}
	fmt.Println()
	return nil
}
