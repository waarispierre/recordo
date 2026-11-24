package main

import (
	"fmt"

	"github.com/waarispierre/recordo/internal/capture"
	"github.com/waarispierre/recordo/internal/config"
	"github.com/waarispierre/recordo/internal/session"
	"github.com/waarispierre/recordo/internal/tools"
)

func cmdWindows() error {
	content, err := capture.ShareableContent()
	if err != nil {
		return err
	}
	windows := capture.Capturable(content.Windows)
	if len(windows) == 0 {
		fmt.Println("  no windows found")
		return nil
	}
	fmt.Printf("\n  %9s  %s\n", "ID", "WINDOW")
	for _, w := range windows {
		fmt.Printf("  %9d  %s\n", w.ID, w.Label())
	}
	fmt.Print("\n  recordo -w <ID>    or    recordo -a <app name>\n\n")
	return nil
}

func cmdDevices() error {
	list := func(title string, ds []capture.Device) {
		fmt.Printf("\n  %s\n", title)
		if len(ds) == 0 {
			fmt.Println("  none found")
		}
		for _, d := range ds {
			fmt.Printf("  · %s\n", d.Name)
		}
	}
	list("microphones", capture.Microphones())
	list("cameras", capture.Cameras())
	fmt.Print("\n  recordo config set audio.device \"<name>\"    or    webcam.device\n\n")
	return nil
}

func check(name string, ok bool, hint string) {
	if ok {
		fmt.Printf("  ✓ %-18s\n", name)
	} else {
		fmt.Printf("  ✗ %-18s %s\n", name, hint)
	}
}

func cmdDoctor(verbose bool) error {
	fmt.Print("\n  recordo doctor\n\n")
	_, err := capture.ShareableContent()
	check("Screen Recording", err == nil, "required — grant it to your terminal, then restart it")
	check("Accessibility", capture.AccessibilityTrusted(),
		"optional — without it, browser pages are cropped by a fixed guess")

	// Read-only: these ask what was already decided and never prompt. A doctor command
	// that pops a permission dialog is one nobody would run twice.
	for _, p := range []struct {
		name   string
		access capture.Access
	}{{"Microphone", capture.MicrophoneAccess()}, {"Camera", capture.CameraAccess()}} {
		mark := "·"
		if p.access == capture.Granted {
			mark = "✓"
		}
		fmt.Printf("  %s %-18s %s\n", mark, p.name, p.access.Explain())
	}

	// Report the absolute binary that will actually run, not merely "something named
	// ffmpeg is on $PATH".
	for _, name := range []string{"ffmpeg", "ffprobe"} {
		if p, ok := tools.Find(name); ok {
			fmt.Printf("  ✓ %-18s %s\n", name, p)
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
	fmt.Printf("\n  config %s\n  videos %s\n", cfg, videos)
	if verbose {
		return browserReport()
	}
	fmt.Println("  --verbose adds per-browser detail")
	fmt.Println()
	return nil
}

// browserReport shows what page bounds each open browser reports, which is what decides
// whether a recording is cropped exactly or by the fixed fallback.
func browserReport() error {
	fmt.Print("\n  browsers\n")
	content, err := capture.ShareableContent()
	if err != nil {
		return err
	}
	found := false
	for _, w := range capture.Capturable(content.Windows) {
		if !config.IsBrowser(w.BundleID) {
			continue
		}
		found = true
		fmt.Printf("  · %-16s", w.AppName)
		if r, ok := capture.WebContentRect(w.PID, w.Frame); ok {
			fmt.Printf("page %.0fx%.0f, chrome %.0fpt exact\n", r.W, r.H, r.Y-w.Frame.Y)
		} else {
			fmt.Println("page bounds unavailable — will use the fixed crop")
		}
	}
	if !found {
		fmt.Println("  · no browser windows open")
	}
	fmt.Println()
	return nil
}
