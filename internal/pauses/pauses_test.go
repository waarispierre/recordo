package pauses

import (
	"math"
	"slices"
	"testing"

	"github.com/waarispierre/recordo/internal/camera"
	"github.com/waarispierre/recordo/internal/telemetry"
)

func TestAFrameInsideAPauseIsCutAndItsEdgesAreHalfOpen(t *testing.T) {
	p := []Pause{{100, 200}}
	for tNs, want := range map[uint64]bool{99: false, 100: true, 199: true, 200: false} {
		if got := IsPausedAt(p, tNs); got != want {
			t.Errorf("IsPausedAt(%d) = %v, want %v", tNs, got, want)
		}
	}
}

func TestRecordedTimeClosesUpThePausesBeforeIt(t *testing.T) {
	p := []Pause{{100, 200}, {300, 350}}
	tests := []struct {
		in, want uint64
		ok       bool
	}{
		{50, 50, true},
		{150, 0, false},
		{200, 100, true}, // resuming picks up exactly where the pause began
		{400, 250, true},
	}
	for _, tt := range tests {
		got, ok := RecordedTime(p, tt.in)
		if got != tt.want || ok != tt.ok {
			t.Errorf("RecordedTime(%d) = %d,%v want %d,%v", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}

func TestAClickZoomHoldsAcrossAPause(t *testing.T) {
	const s = uint64(1_000_000_000)
	// Click at 1s, pause 1.2s..21.2s, resume. Without the remap the 1.3s hold expires
	// during the pause and the first frame back is zoomed out.
	p := []Pause{{1_200_000_000, 21_200_000_000}}
	var tel telemetry.Telemetry
	tel.TapInstalled = true
	for _, e := range []telemetry.Event{
		{TNs: 0, Kind: telemetry.Move, X: 500, Y: 500},
		{TNs: s, Kind: telemetry.Down, X: 500, Y: 500},
		{TNs: s + 50_000_000, Kind: telemetry.Up, X: 500, Y: 500},
	} {
		if rt, ok := RecordedTime(p, e.TNs); ok {
			e.TNs = rt
			tel.Events = append(tel.Events, e)
		}
	}
	var times, raw []uint64
	for i := range uint64(1400) {
		if rt, ok := RecordedTime(p, i*s/60); ok {
			times = append(times, rt)
			raw = append(raw, i*s/60)
		}
	}
	crops := camera.Solve(&tel, times, 1000, 1000, camera.DefaultConfig())

	// Last frame before the pause, and first frame after it.
	before := len(raw) - 1
	for raw[before] >= 1_200_000_000 {
		before--
	}
	zoom := func(c camera.Crop) float64 { return 1000 / c.W }
	if zoom(crops[before]) <= 1 {
		t.Fatal("should be zoomed in before the pause")
	}
	if d := math.Abs(zoom(crops[before+1]) - zoom(crops[before])); d >= 0.05 {
		t.Fatalf("zoom jumped %.3f across the pause", d)
	}
}

func TestStoppingWhilePausedClosesThePauseAtTheStop(t *testing.T) {
	start := uint64(500)
	l := &Log{open: &start}
	if got := l.Finish(900); !slices.Equal(got, []Pause{{500, 900}}) {
		t.Fatalf("got %v", got)
	}
}

func TestToggle(t *testing.T) {
	now := uint64(0)
	l := NewLog(func() uint64 { return now })
	now = 10
	if !l.Toggle() || !l.IsPaused() {
		t.Fatal("first toggle should pause")
	}
	now = 25
	if l.PausedNs() != 15 {
		t.Fatalf("open pause should count, got %d", l.PausedNs())
	}
	if l.Toggle() {
		t.Fatal("second toggle should resume")
	}
	now = 40
	if l.PausedNs() != 15 {
		t.Fatalf("closed pause total = %d", l.PausedNs())
	}
}

func TestAudioCutsAreRelativeToTheFirstFrame(t *testing.T) {
	const base = 10_000_000_000
	got := AudioSelect([]Pause{{base + 2_000_000_000, base + 3_500_000_000}}, base)
	if want := "aselect='not(between(t,2.000000,3.500000))',asetpts=N/SR/TB"; got != want {
		t.Fatalf("got %q", got)
	}
	if AudioSelect(nil, base) != "" {
		t.Fatal("no pauses should produce no filter")
	}
}
