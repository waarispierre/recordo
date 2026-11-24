package record

import (
	"strings"
	"testing"
)

func TestHealthLines(t *testing.T) {
	got := healthLines(Health{Frames: 3, CursorSamples: 1, MedianGapMs: 500})
	want := []string{
		"3 frames · 1 cursor samples · 0 clicks",
		"clicks were not recorded — grant Input Monitoring for click-zoom",
		"the cursor never moved, so there is nothing to zoom toward",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got %q", got)
	}
}
