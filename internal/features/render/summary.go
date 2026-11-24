package render

import "fmt"

// Summary is the one-line account of a render the TUI's reports show:
// "1920x1080 · 900 frames · 6.1x realtime · voice over · webcam".
func (r Report) Summary() string {
	extra := ""
	if r.Audio {
		extra += " · voice over"
	}
	if r.Webcam {
		extra += " · webcam"
	}
	return fmt.Sprintf("%dx%d · %d frames · %.1fx realtime%s", r.OutW, r.OutH, r.Frames, r.RealtimeRatio(), extra)
}
