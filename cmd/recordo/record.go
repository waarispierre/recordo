package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/spf13/cobra"

	"github.com/waarispierre/recordo/internal/domain/pauses"
	"github.com/waarispierre/recordo/internal/domain/session"
	"github.com/waarispierre/recordo/internal/features/record"
	"github.com/waarispierre/recordo/internal/platform/macos"
)

func cmdRecord(cmd *cobra.Command) error {
	cfg, _, err := loadConfig()
	if err != nil {
		return err
	}
	if z := zoomOverride(cmd); z != nil {
		cfg.Camera.ZoomPercent = *z
	}
	if m := override(opt.mic, opt.noMic); m != nil {
		cfg.Audio.Microphone = *m
	}
	webcam := override(opt.webcam, opt.noWebcam)
	if webcam != nil {
		cfg.Webcam.Enabled = *webcam
	}
	s, err := session.Create()
	if err != nil {
		return err
	}
	banner()
	stdin := bufio.NewReader(os.Stdin)

	err = record.Record(s, target(), cfg, record.RecordOptions{
		Choose: func(ws []macos.Window) (macos.Window, bool, error) { return chooseWindow(stdin, ws) },
		OnStart: func(p *record.Plan) {
			say("%s %s", green.Bold(true).Render("recording"), dim.Render(p.Label))
			say("%s %s", "         ", dim.Render(fmt.Sprintf("%dx%d at %dx", p.CaptureW, p.CaptureH, p.Scale)))
			if p.Microphone != "" {
				say("%s %s", dim.Render("voice over"), dim.Render(p.Microphone))
			}
			if p.Webcam != "" {
				say("%s %s", dim.Render("webcam"), dim.Render(p.Webcam))
			}
			fmt.Println()
		},
		Stop: func(*pauses.Log) { waitForStop(stdin, opt.seconds) },
		Warn: func(w record.Warning) { say("%s %s", warn, string(w)) },
	})
	if err != nil {
		return err
	}

	h, err := record.CheckHealth(s)
	if err != nil {
		return err
	}
	healthReport(h)
	if opt.noRender {
		say("%s %s", cyan.Render("saved"), s.Dir)
		return nil
	}
	// The one-off --webcam/--no-webcam carries into the render, so a recording made with
	// the webcam gets its overlay without the config having to say so.
	return renderSession(s, s.Export(), zoomOverride(cmd), webcam)
}

func banner() {
	fmt.Println()
	say("%s %s", bold.Render("recordo"), dim.Render(version))
}

// chooseWindow is a numbered picker, kept deliberately plain so it works over ssh and in
// dumb terminals. 0 or an empty answer means the whole display.
func chooseWindow(in *bufio.Reader, windows []macos.Window) (macos.Window, bool, error) {
	if len(windows) == 0 {
		say("no windows found — recording the whole display")
		return macos.Window{}, false, nil
	}
	fmt.Println()
	say("%s", bold.Render("what should I record?"))
	fmt.Println()
	say(" %s  entire display", cyan.Render("0"))
	for i, w := range windows {
		say("%s  %s", cyan.Render(fmt.Sprintf("%2d", i+1)), record.Label(w))
	}
	lipgloss.Printf("\n  %s ", bold.Render("choice [0]:"))
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
			lipgloss.Printf("\r  %s %02d:%02d  %s", red.Render(frames[i%len(frames)]),
				int(e.Minutes()), int(e.Seconds())%60, dim.Render(hint))
			continue
		}
		lipgloss.Printf("\r\033[K  %s %.1fs captured\n", tick, time.Since(start).Seconds())
		return
	}
}

func healthReport(h record.Health) {
	say("%s %d frames · %d cursor samples · %d clicks", dot, h.Frames, h.CursorSamples, h.Clicks)
	if !(h.MedianGapMs < 100) && h.CursorSamples > 10 {
		say("%s cursor timing is off by %.0fms — zoom may lag the pointer", warn, h.MedianGapMs)
	}
	if !h.TapInstalled {
		say("%s clicks were not recorded — grant Input Monitoring for click-zoom", warn)
	}
	if h.CursorSamples <= 1 {
		say("%s the cursor never moved, so there is nothing to zoom toward", warn)
	}
}

// openFile shows a finished video. /usr/bin/open launches a separate app and returns at
// once.
func openFile(path string) {
	if !opt.noOpen {
		_ = exec.Command("/usr/bin/open", path).Run()
	}
}
