// Command recordo records demos with a cursor-following camera.
//
// During the Go rewrite this binary only carries the subcommands ported so far; the Rust
// build is still the complete tool. See docs/proposals/go-rewrite.md.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/waarispierre/recordo/internal/render"
	"github.com/waarispierre/recordo/internal/session"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "\n  error: %v\n\n", err)
		os.Exit(1)
	}
}

const usage = `usage:
  recordo render [-zoom N] [-o FILE] [SESSION_DIR]
  recordo record [-a APP | -w ID | -display] [-s SECONDS]
  recordo windows
  recordo devices
  recordo doctor [-verbose]`

func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("%s", usage)
	}
	switch args[0] {
	case "render":
		return cmdRender(args[1:])
	case "record":
		return cmdRecord(args[1:])
	case "windows":
		return cmdWindows()
	case "devices":
		return cmdDevices()
	case "doctor":
		fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
		verbose := fs.Bool("verbose", false, "per-browser page-bounds detail")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		return cmdDoctor(*verbose)
	}
	return fmt.Errorf("%s", usage)
}

func cmdRender(args []string) error {
	fs := flag.NewFlagSet("render", flag.ContinueOnError)
	zoom := fs.Float64("zoom", -1, "zoom percent for this render only")
	out := fs.String("o", "", "output file (default: export.mp4 in the session)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	var s *session.Session
	var err error
	if p := fs.Arg(0); p != "" {
		s, err = session.Open(p)
	} else {
		s, err = session.Latest()
	}
	if err != nil {
		return err
	}
	dst := s.Export()
	if *out != "" {
		if dst, err = filepath.Abs(*out); err != nil {
			return err
		}
	}

	opts := render.Options{Progress: func(done, total int) {
		if done%30 == 0 || done == total {
			fmt.Printf("\r  rendering %d/%d", done, total)
		}
	}}
	if *zoom >= 0 {
		opts.ZoomPercent = zoom
	}
	fmt.Printf("  rendering %s\n", s.Dir)
	rep, err := render.Run(s.Capture(), dst, opts)
	if err != nil {
		return err
	}
	fmt.Printf("\n  %d frames, %dx%d, %.1fs video in %.1fs (%.2fx realtime, %.0f fps)\n  -> %s\n",
		rep.Frames, rep.OutW, rep.OutH, rep.VideoSeconds, rep.RenderSeconds,
		rep.RealtimeRatio(), rep.FPS(), dst)
	return nil
}
