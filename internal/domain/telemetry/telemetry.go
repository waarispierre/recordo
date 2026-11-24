// Package telemetry holds the cursor and click data captured alongside a recording.
//
// ScreenCaptureKit exposes no per-frame cursor metadata, so cursor state is captured out
// of band and correlated with frames by mach timestamp. This package is only the data
// and its telemetry.json shape; capturing and file I/O live elsewhere.
package telemetry

// PollHz is how often the cursor position is sampled.
const PollHz = 120

// EventKind is serialised in snake_case to match the Rust version's telemetry.json.
type EventKind string

const (
	Move   EventKind = "move"
	Down   EventKind = "down"
	Up     EventKind = "up"
	Scroll EventKind = "scroll"
)

// Event is one cursor sample or mouse event.
type Event struct {
	// TNs is mach absolute time in nanoseconds — the same timebase as frame display times.
	TNs  uint64    `json:"t_ns"`
	Kind EventKind `json:"kind"`
	// X and Y are global display coordinates, top-left origin.
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// Telemetry is the contents of telemetry.json.
type Telemetry struct {
	Events []Event `json:"events"`
	// TapInstalled reports whether the event tap installed. False means Input Monitoring
	// was denied and no clicks could have been seen; true with zero clicks simply means
	// none happened.
	TapInstalled bool `json:"tap_installed"`
}

// Count returns how many events are of the given kind.
func (t *Telemetry) Count(kind EventKind) int {
	n := 0
	for _, e := range t.Events {
		if e.Kind == kind {
			n++
		}
	}
	return n
}
