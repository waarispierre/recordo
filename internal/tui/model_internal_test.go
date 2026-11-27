package tui

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestCompressIndicatorIsAsWideAsTheRenderBar(t *testing.T) {
	frames := sweepFrames(barWidth, 6)
	if len(frames) != 2*(barWidth-6) {
		t.Fatalf("%d frames", len(frames))
	}
	for i, f := range frames {
		if n := utf8.RuneCountInString(f); n != barWidth {
			t.Fatalf("frame %d is %d cells: %q", i, n, f)
		}
		if strings.Count(f, "▰") != 6 {
			t.Fatalf("frame %d segment: %q", i, f)
		}
	}
	if !strings.HasPrefix(frames[0], "▰") || !strings.HasSuffix(frames[barWidth-6], "▰") {
		t.Fatalf("segment does not reach both ends: %q … %q", frames[0], frames[barWidth-6])
	}
}
