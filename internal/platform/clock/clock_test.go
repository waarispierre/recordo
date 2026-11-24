package clock

import "testing"

func TestScaleDoesNotOverflowOnAppleSiliconRatio(t *testing.T) {
	// A week of uptime in 24 MHz ticks: t*125 overflows uint64, the result must not.
	ticks := uint64(24_000_000) * 60 * 60 * 24 * 7 * 1000
	got := scale(ticks, 125, 3)
	want := ticks / 3 * 125
	if got != want {
		t.Fatalf("scale = %d, want %d", got, want)
	}
}

func TestNowIsMonotonic(t *testing.T) {
	a := NowNanos()
	b := NowNanos()
	if b < a {
		t.Fatalf("clock went backwards: %d then %d", a, b)
	}
}
