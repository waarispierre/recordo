// Package pauses handles pausing a recording.
//
// ScreenCaptureKit's recording output cannot be paused, and stopping and restarting it
// would leave several files to stitch back together. So capture never actually stops:
// the pauses are logged on the shared mach timebase and the render cuts them out of the
// screen, the webcam and the voice over alike. The raw capture still holds what happened
// while paused, the same way it still holds the browser chrome the render removes.
package pauses

import (
	"fmt"
	"strings"
)

// Pause is one paused span, in mach nanoseconds — the same timebase as frame display times.
type Pause struct {
	StartNs uint64 `json:"start_ns"`
	EndNs   uint64 `json:"end_ns"`
}

// Log records the pauses taken during one recording.
type Log struct {
	now  func() uint64
	done []Pause
	open *uint64
}

// NewLog returns a log that reads the time from now (clock.NowNanos in production).
func NewLog(now func() uint64) *Log { return &Log{now: now} }

// IsPaused reports whether a pause is in progress.
func (l *Log) IsPaused() bool { return l.open != nil }

// Toggle pauses if running, resumes if paused. It returns whether it is now paused.
func (l *Log) Toggle() bool {
	now := l.now()
	if l.open != nil {
		l.done = append(l.done, Pause{StartNs: *l.open, EndNs: now})
		l.open = nil
		return false
	}
	l.open = &now
	return true
}

// PausedNs is the total time spent paused so far, including a pause still in progress.
func (l *Log) PausedNs() uint64 {
	var total uint64
	for _, p := range l.done {
		total += p.EndNs - p.StartNs
	}
	if l.open != nil {
		if now := l.now(); now > *l.open {
			total += now - *l.open
		}
	}
	return total
}

// Finish returns every pause, closing one still open at endNs — stopping while paused
// means the tail is not wanted either.
func (l *Log) Finish(endNs uint64) []Pause {
	out := append([]Pause(nil), l.done...)
	if l.open != nil {
		out = append(out, Pause{StartNs: *l.open, EndNs: endNs})
	}
	return out
}

// IsPausedAt reports whether tNs falls inside any of pauses. Spans are half-open.
func IsPausedAt(pauses []Pause, tNs uint64) bool {
	for _, p := range pauses {
		if p.StartNs <= tNs && tNs < p.EndNs {
			return true
		}
	}
	return false
}

// RecordedTime maps a capture time onto the recorded timeline — the one with the pauses
// taken out. ok is false when tNs falls inside a pause.
//
// Anything timed in seconds of video, like how long a click zoom holds, has to run on this
// timeline; on the capture's own, a pause would quietly use the time up.
func RecordedTime(pauses []Pause, tNs uint64) (t uint64, ok bool) {
	if IsPausedAt(pauses, tNs) {
		return 0, false
	}
	var before uint64
	for _, p := range pauses {
		if p.EndNs <= tNs {
			before += p.EndNs - p.StartNs
		}
	}
	return tNs - before, true
}

// AudioSelect returns an ffmpeg aselect expression that drops the paused spans of an audio
// stream whose t=0 sits at baseNs, or "" when nothing needs cutting.
func AudioSelect(pauses []Pause, baseNs uint64) string {
	secs := func(ns uint64) float64 {
		if ns < baseNs {
			return 0
		}
		return float64(ns-baseNs) / 1e9
	}
	var spans []string
	for _, p := range pauses {
		if p.EndNs > baseNs {
			spans = append(spans, fmt.Sprintf("between(t,%.6f,%.6f)", secs(p.StartNs), secs(p.EndNs)))
		}
	}
	if len(spans) == 0 {
		return ""
	}
	// asetpts closes the gaps aselect leaves, or the encoder pads them with silence.
	return fmt.Sprintf("aselect='not(%s)',asetpts=N/SR/TB", strings.Join(spans, "+"))
}
