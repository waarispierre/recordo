package tui

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"github.com/waarispierre/recordo/internal/platform/macos"
)

// grey is a flat mid-grey frame, so a rendered cell is only ever "drawn" or "masked".
func grey(w, h uint32) macos.PreviewFrame {
	return macos.PreviewFrame{RGB: bytes.Repeat([]byte{128}, int(w*h*3)), W: w, H: h}
}

func shape(cells [][]sextantCell) []string {
	out := make([]string, len(cells))
	for i, row := range cells {
		var b strings.Builder
		for _, c := range row {
			b.WriteRune(c.glyph)
		}
		out[i] = b.String()
	}
	return out
}

func TestARectangularOverlayFillsEveryCell(t *testing.T) {
	// A flat frame has no luminance split to make, so every cell renders solid.
	got := shape(renderSextants(grey(32, 24), 10, 4, false, false, false))
	want := slices.Repeat([]string{strings.Repeat("█", 10)}, 4)
	if !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestEverySextantMaskMapsToADistinctGlyph(t *testing.T) {
	glyphs := map[rune]bool{}
	for m := range 64 {
		glyphs[sextant(uint8(m))] = true
	}
	if len(glyphs) != 64 {
		t.Fatalf("two masks share a glyph: %d distinct", len(glyphs))
	}
	if sextant(0) != ' ' || sextant(0b111111) != '█' {
		t.Fatal("empty and full masks should be space and full block")
	}
	// The first and last of the dedicated sextant code points.
	if sextant(0b000001) != '\U0001FB00' || sextant(0b111110) != '\U0001FB3B' {
		t.Fatalf("got %U and %U", sextant(0b000001), sextant(0b111110))
	}
}

func TestACircularOverlayIsMaskedSymmetrically(t *testing.T) {
	const rows = 5
	const cols = rows * 2
	drawn := shape(renderSextants(grey(32, 32), cols, rows, true, false, false))

	// Flipping a row means reversing the cells *and* mirroring each glyph, since a
	// sextant carries its own left/right halves.
	maskOf := map[rune]uint8{}
	for m := range 64 {
		maskOf[sextant(uint8(m))] = uint8(m)
	}
	swap := func(m uint8, lo, hi uint) uint8 {
		return (m&(1<<lo))<<(hi-lo) | (m&(1<<hi))>>(hi-lo)
	}
	mirrorGlyph := func(c rune) rune {
		m := maskOf[c]
		return sextant(swap(m, 0, 1) | swap(m, 2, 3) | swap(m, 4, 5))
	}

	for i, line := range drawn {
		chars := []rune(line)
		if len(chars) != cols {
			t.Fatalf("row %d has %d cells, want %d", i, len(chars), cols)
		}
		// A lopsided circle is the bug this guards against.
		flipped := make([]rune, len(chars))
		for j, c := range chars {
			flipped[len(chars)-1-j] = mirrorGlyph(c)
		}
		if string(flipped) != line {
			t.Errorf("row %d is not left-right symmetric: %q vs %q", i, line, string(flipped))
		}
	}

	// The corners are outside the circle and the middle row is solid.
	if !strings.HasPrefix(drawn[0], " ") {
		t.Errorf("top-left corner should be masked: %q", drawn[0])
	}
	if drawn[rows/2] != strings.Repeat("█", cols) {
		t.Errorf("middle row should be solid: %q", drawn[rows/2])
	}
}

func TestMirroringDoesNotChangeWhichCellsAreDrawn(t *testing.T) {
	plain := shape(renderSextants(grey(32, 32), 12, 6, true, false, false))
	mirrored := shape(renderSextants(grey(32, 32), 12, 6, true, false, true))
	if !slices.Equal(plain, mirrored) {
		t.Fatalf("%q != %q", plain, mirrored)
	}
}

// Not in the Rust suite: a cell with real contrast splits into a glyph with two colours,
// the brighter half in front.
func TestAContrastingCellSplitsIntoTwoColours(t *testing.T) {
	// Left column white, right column black: every cell is the left-half block.
	f := macos.PreviewFrame{W: 2, H: 1, RGB: []byte{255, 255, 255, 0, 0, 0}}
	cells := renderSextants(f, 1, 1, false, false, false)
	c := cells[0][0]
	if c.glyph != '▌' || !c.hasBG || c.fg != (rgb{255, 255, 255}) || c.bg != (rgb{0, 0, 0}) {
		t.Fatalf("got %+v", c)
	}
	// Mirrored, the bright half swaps sides.
	if g := renderSextants(f, 1, 1, false, false, true)[0][0].glyph; g != '▐' {
		t.Fatalf("mirrored glyph %q", g)
	}
}
