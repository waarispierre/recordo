package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"

	"github.com/waarispierre/recordo/internal/capture"
	"github.com/waarispierre/recordo/internal/config"
	"github.com/waarispierre/recordo/internal/pauses"
	"github.com/waarispierre/recordo/internal/session"
)

func cmdRecord(args []string) error {
	fs := flag.NewFlagSet("record", flag.ContinueOnError)
	app := fs.String("a", "", "record the largest window of this app")
	windowID := fs.Uint("w", 0, "record this window id (see `recordo windows`)")
	display := fs.Bool("display", false, "record the whole display")
	seconds := fs.Uint("s", 0, "stop after this many seconds")
	if err := fs.Parse(args); err != nil {
		return err
	}
	target := capture.Target{Kind: capture.TargetAsk}
	switch {
	case *display:
		target.Kind = capture.TargetDisplay
	case *app != "":
		target = capture.Target{Kind: capture.TargetApp, App: *app}
	case *windowID != 0:
		target = capture.Target{Kind: capture.TargetWindow, WindowID: uint32(*windowID)}
	}

	cfgPath, err := session.ConfigPath()
	if err != nil {
		return err
	}
	cfg, err := config.LoadOrCreate(cfgPath)
	if err != nil {
		return err
	}
	s, err := session.Create()
	if err != nil {
		return err
	}
	stdin := bufio.NewReader(os.Stdin)

	err = capture.Record(s, target, cfg,
		func(ws []capture.Window) (capture.Window, bool, error) { return chooseWindow(stdin, ws) },
		func(p *capture.Plan) {
			fmt.Printf("\n  ● recording %s\n", p.Label)
			if p.Microphone != "" {
				fmt.Printf("  · voice over: %s\n", p.Microphone)
			}
		},
		func(*pauses.Log) { waitForStop(stdin, *seconds) },
		func(w capture.Warning) { fmt.Printf("  ! %s\n", w) },
	)
	if err != nil {
		return err
	}

	h, err := capture.CheckHealth(s)
	if err != nil {
		return err
	}
	fmt.Printf("\n  · %d frames · %d cursor samples · %d clicks · median cursor gap %.1fms\n",
		h.Frames, h.CursorSamples, h.Clicks, h.MedianGapMs)
	if h.CursorSamples > 10 && !(h.MedianGapMs < 100) {
		fmt.Printf("  ! cursor timing is off by %.0fms — zoom may lag the pointer\n", h.MedianGapMs)
	}
	fmt.Printf("  saved %s\n\n", s.Dir)
	return nil
}

// chooseWindow asks which window to record; 0 or an empty answer means the whole display.
func chooseWindow(in *bufio.Reader, windows []capture.Window) (capture.Window, bool, error) {
	if len(windows) == 0 {
		fmt.Println("  no windows found — recording the whole display")
		return capture.Window{}, false, nil
	}
	fmt.Print("\n  what should I record?\n\n   0  entire display\n")
	for i, w := range windows {
		fmt.Printf("  %2d  %s\n", i+1, w.Label())
	}
	fmt.Print("\n  choice [0]: ")
	line, _ := in.ReadString('\n')
	choice, _ := strconv.Atoi(strings.TrimSpace(line))
	if choice <= 0 {
		return capture.Window{}, false, nil
	}
	if choice > len(windows) {
		return capture.Window{}, false, fmt.Errorf("no option %d", choice)
	}
	return windows[choice-1], true, nil
}

// waitForStop blocks until Enter, Ctrl-C, or the time limit. Ctrl-C ends the recording
// rather than killing the process, so the capture is still finalised and the sidecars
// written.
func waitForStop(in *bufio.Reader, seconds uint) {
	done := make(chan struct{}, 2)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	defer signal.Stop(sig)
	go func() { <-sig; done <- struct{}{} }()

	var limit <-chan time.Time
	if seconds > 0 {
		limit = time.After(time.Duration(seconds) * time.Second)
		fmt.Printf("  stopping after %ds\n", seconds)
	} else {
		go func() { _, _ = in.ReadString('\n'); done <- struct{}{} }()
		fmt.Println("  press Enter to stop")
	}
	start := time.Now()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-done:
			fmt.Println()
			return
		case <-limit:
			fmt.Println()
			return
		case <-tick.C:
			fmt.Printf("\r  %s", time.Since(start).Truncate(time.Second))
		}
	}
}
