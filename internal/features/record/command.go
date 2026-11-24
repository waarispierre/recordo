package record

import (
	"bufio"
	"fmt"
	"github.com/waarispierre/recordo/internal/cli"
	"github.com/waarispierre/recordo/internal/features/render"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/spf13/cobra"

	"github.com/waarispierre/recordo/internal/domain/pauses"
	"github.com/waarispierre/recordo/internal/domain/session"
	"github.com/waarispierre/recordo/internal/platform/macos"
)

func RunCLI(cmd *cobra.Command) error {
	cfg, _, err := cli.LoadConfig()
	if err != nil {
		return err
	}
	if z := cli.ZoomOverride(cmd); z != nil {
		cfg.Camera.ZoomPercent = *z
	}
	if m := cli.Override(cli.Opt.Mic, cli.Opt.NoMic); m != nil {
		cfg.Audio.Microphone = *m
	}
	webcam := cli.Override(cli.Opt.Webcam, cli.Opt.NoWebcam)
	if webcam != nil {
		cfg.Webcam.Enabled = *webcam
	}
	s, err := session.Create()
	if err != nil {
		return err
	}
	banner()
	stdin := bufio.NewReader(os.Stdin)

	err = Record(s, targetFromFlags(), cfg, RecordOptions{
		Choose: func(ws []macos.Window) (macos.Window, bool, error) { return chooseWindow(stdin, ws) },
		OnStart: func(p *Plan) {
			cli.Say("%s %s", cli.Green.Bold(true).Render("recording"), cli.Dim.Render(p.Label))
			cli.Say("%s %s", "         ", cli.Dim.Render(fmt.Sprintf("%dx%d at %dx", p.CaptureW, p.CaptureH, p.Scale)))
			if p.Microphone != "" {
				cli.Say("%s %s", cli.Dim.Render("voice over"), cli.Dim.Render(p.Microphone))
			}
			if p.Webcam != "" {
				cli.Say("%s %s", cli.Dim.Render("webcam"), cli.Dim.Render(p.Webcam))
			}
			fmt.Println()
		},
		Stop: func(*pauses.Log) { waitForStop(stdin, cli.Opt.Seconds) },
		Warn: func(w Warning) { cli.Say("%s %s", cli.Warn, string(w)) },
	})
	if err != nil {
		return err
	}

	h, err := CheckHealth(s)
	if err != nil {
		return err
	}
	healthReport(h)
	if cli.Opt.NoRender {
		cli.Say("%s %s", cli.Cyan.Render("saved"), s.Dir)
		return nil
	}
	// The one-off --webcam/--no-webcam carries into the render, so a recording made with
	// the webcam gets its overlay without the config having to say so.
	return render.Present(s, s.Export(), cli.ZoomOverride(cmd), webcam)
}

func banner() {
	fmt.Println()
	cli.Say("%s %s", cli.Bold.Render(""), cli.Dim.Render(cli.Version))
}

// chooseWindow is a numbered picker, kept deliberately plain so it works over ssh and in
// dumb terminals. 0 or an empty answer means the whole display.
func chooseWindow(in *bufio.Reader, windows []macos.Window) (macos.Window, bool, error) {
	if len(windows) == 0 {
		cli.Say("no windows found — recording the whole display")
		return macos.Window{}, false, nil
	}
	fmt.Println()
	cli.Say("%s", cli.Bold.Render("what should I record?"))
	fmt.Println()
	cli.Say(" %s  entire display", cli.Cyan.Render("0"))
	for i, w := range windows {
		cli.Say("%s  %s", cli.Cyan.Render(fmt.Sprintf("%2d", i+1)), Label(w))
	}
	lipgloss.Printf("\n  %s ", cli.Bold.Render("choice [0]:"))
	line, _ := in.ReadString('\n')
	choice, _ := strconv.Atoi(strings.TrimSpace(line))
	if choice <= 0 {
		return macos.Window{}, false, nil
	}
	if choice > len(windows) {
		return macos.Window{}, false, fmt.Errorf("no option %d", choice)
	}
	return windows[choice-1], true, nil
}

// waitForStop blocks until Enter, Ctrl-C, or the time limit, showing elapsed time. Ctrl-C
// ends the recording rather than killing the process, so the capture is still finalised
// and the sidecars written.
func waitForStop(in *bufio.Reader, seconds uint) {
	done := make(chan struct{}, 2)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	defer signal.Stop(sig)
	go func() {
		if _, ok := <-sig; ok {
			done <- struct{}{}
		}
	}()

	hint := "press Enter to stop"
	var limit <-chan time.Time
	if seconds > 0 {
		hint = fmt.Sprintf("stopping after %ds", seconds)
		limit = time.After(time.Duration(seconds) * time.Second)
	} else {
		go func() { _, _ = in.ReadString('\n'); done <- struct{}{} }()
	}

	frames := []string{"●", "●", "◐", "◑", "◒", "◓"}
	start := time.Now()
	ticker := time.NewTicker(90 * time.Millisecond)
	defer ticker.Stop()
	for i := 0; ; i++ {
		select {
		case <-done:
		case <-limit:
		case <-ticker.C:
			e := time.Since(start)
			lipgloss.Printf("\r  %s %02d:%02d  %s", cli.Red.Render(frames[i%len(frames)]),
				int(e.Minutes()), int(e.Seconds())%60, cli.Dim.Render(hint))
			continue
		}
		lipgloss.Printf("\r\033[K  %s %.1fs captured\n", cli.Tick, time.Since(start).Seconds())
		return
	}
}

func healthReport(h Health) {
	cli.Say("%s %d frames · %d cursor samples · %d clicks", cli.Dot, h.Frames, h.CursorSamples, h.Clicks)
	if !(h.MedianGapMs < 100) && h.CursorSamples > 10 {
		cli.Say("%s cursor timing is off by %.0fms — zoom may lag the pointer", cli.Warn, h.MedianGapMs)
	}
	if !h.TapInstalled {
		cli.Say("%s clicks were not recorded — grant Input Monitoring for click-zoom", cli.Warn)
	}
	if h.CursorSamples <= 1 {
		cli.Say("%s the cursor never moved, so there is nothing to zoom toward", cli.Warn)
	}
}

// Command is `recordo record`, which a bare `recordo` outside a terminal also runs.
func Command() *cobra.Command {
	return &cobra.Command{
		Use: "record", Short: "Record and render (the default)", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return RunCLI(cmd) },
	}
}

// targetFromFlags turns -d / -w / -a into what to record; none of them means ask.
func targetFromFlags() Target {
	switch {
	case cli.Opt.Display:
		return Target{Kind: TargetDisplay}
	case cli.Opt.Window != 0:
		return Target{Kind: TargetWindow, WindowID: cli.Opt.Window}
	case cli.Opt.App != "":
		return Target{Kind: TargetApp, App: cli.Opt.App}
	}
	return Target{Kind: TargetAsk}
}

// AnyTarget reports whether a targeting flag was given, which means the one-shot path
// rather than the full-screen app.
func AnyTarget() bool { return cli.Opt.App != "" || cli.Opt.Window != 0 || cli.Opt.Display }
