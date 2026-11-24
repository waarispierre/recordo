package tui

import (
	"image/color"
	"math"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/waarispierre/recordo/internal/domain/config"
	"github.com/waarispierre/recordo/internal/domain/pip"
	"github.com/waarispierre/recordo/internal/platform/macos"
)

type rgb struct{ r, g, b uint8 }

func (c rgb) colour() color.Color { return color.RGBA{c.r, c.g, c.b, 0xff} }

// sextantCell is one terminal cell of a rendered thumbnail. A cell with no foreground is
// transparent.
type sextantCell struct {
	glyph        rune
	fg, bg       rgb
	hasFG, hasBG bool
}

// drawWebcamPip floats a small live webcam thumbnail in one corner of area, positioned,
// sized and shaped the same way the export's picture-in-picture is configured — reusing
// pip.PlaceRect with terminal-cell dimensions in place of output pixels, rather than
// reinventing the corner math.
func drawWebcamPip(c *lipgloss.Canvas, area rect, cfg config.WebcamSettings, thumb macos.PreviewFrame) {
	if area.w < 8 || area.h < 5 || thumb.W == 0 || thumb.H == 0 {
		return
	}
	corner := pip.ParseCorner(cfg.Position)
	circle := cfg.Shape == "circle"
	// A circular overlay is cropped to a square before the renderer masks it (see
	// pip.PlaceRect), so the preview crops the same way — otherwise it would show framing
	// at the edges that the finished video does not have.
	aspect := float32(1)
	if !circle {
		aspect = float32(thumb.W) / float32(thumb.H)
	}
	// A cell is about twice as tall as it is wide, and the half-block trick fits two
	// source rows into each one, so a frame needs double the columns to not look squashed.
	aspect *= 2
	// The configured percentage is authored against an exported frame of a thousand-odd
	// pixels; in a couple of dozen terminal rows the same number is too small to frame a
	// face by, so it only steers within a range big enough to actually judge framing.
	sizePercent := max(32, min(cfg.SizePercent, 52))
	r := pip.PlaceRect(float32(area.w), float32(area.h), corner, 1.0, 0, 0, sizePercent, aspect)

	round := func(v float32) int { return int(math.Round(float64(v))) }
	x := area.x + min(round(r.X), max(area.w-4, 0))
	y := area.y + min(round(r.Y), max(area.h-3, 0))
	box := rect{
		x: x,
		y: y,
		w: min(max(round(r.W), 4), max(area.w-(x-area.x), 0)),
		h: min(max(round(r.H), 3), max(area.h-(y-area.y), 0)),
	}
	roundCorners := !circle && cfg.CornerRadius > 0
	put(c, box, sextantString(renderSextants(thumb, box.w, box.h, circle, roundCorners, cfg.Mirror)))
}

// renderSextants renders thumb into cols x rows terminal cells using 2x3 sextant glyphs.
//
// Six samples per cell rather than the two a half block carries, which is what stops a
// face reading as pixel art. A cell can still only hold two colours, so each one's
// samples are split at the midpoint of their own luminance range: the brighter group
// becomes the foreground and picks the glyph, the darker becomes the background.
//
// circle masks against the box's inscribed circle and roundCorners approximates
// corner_radius, both evaluated per sample — a partly covered cell keeps the samples that
// are inside and leaves the rest transparent, which is what smooths the edge.
func renderSextants(thumb macos.PreviewFrame, cols, rows int, circle, roundCorners, mirror bool) [][]sextantCell {
	cols, rows = max(cols, 1), max(rows, 1)
	subW, subH := float32(cols*2), float32(rows*3)

	// A circle is masked out of a centred square, the same crop the renderer applies, so
	// the two agree on what is actually in frame.
	winX, winY, winW, winH := uint32(0), uint32(0), thumb.W, thumb.H
	if circle {
		side := min(thumb.W, thumb.H)
		winX, winY, winW, winH = (thumb.W-side)/2, (thumb.H-side)/2, side, side
	}

	sample := func(sx, sy int) rgb {
		nx := (float32(sx) + 0.5) / subW
		if mirror {
			nx = 1 - nx
		}
		ny := (float32(sy) + 0.5) / subH
		px := winX + min(uint32(nx*float32(winW)), winW-1)
		py := winY + min(uint32(ny*float32(winH)), winH-1)
		i := (py*thumb.W + px) * 3
		return rgb{thumb.RGB[i], thumb.RGB[i+1], thumb.RGB[i+2]}
	}

	shown := func(sx, sy int) bool {
		x := (float32(sx)+0.5)/subW - 0.5
		y := (float32(sy)+0.5)/subH - 0.5
		if roundCorners && abs32(x) > 0.45 && abs32(y) > 0.4 {
			return false
		}
		return !circle || x*x+y*y <= 0.25
	}

	// Bit order matches the sextant code points: top-left, top-right, middle-left,
	// middle-right, bottom-left, bottom-right.
	offsets := [6][2]int{{0, 0}, {1, 0}, {0, 1}, {1, 1}, {0, 2}, {1, 2}}
	out := make([][]sextantCell, rows)
	visible := make([]bitSample, 0, 6)
	for row := range rows {
		out[row] = make([]sextantCell, cols)
		for col := range cols {
			visible = visible[:0]
			for bit, d := range offsets {
				sx, sy := col*2+d[0], row*3+d[1]
				if shown(sx, sy) {
					visible = append(visible, bitSample{uint8(bit), sample(sx, sy)})
				}
			}
			out[row][col] = sextantOf(visible)
		}
	}
	return out
}

type bitSample struct {
	bit uint8
	c   rgb
}

func abs32(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}

func luma(c rgb) float32 {
	return 0.299*float32(c.r) + 0.587*float32(c.g) + 0.114*float32(c.b)
}

func mean(group []rgb) rgb {
	n := uint32(max(len(group), 1))
	var r, g, b uint32
	for _, c := range group {
		r, g, b = r+uint32(c.r), g+uint32(c.g), b+uint32(c.b)
	}
	return rgb{uint8(r / n), uint8(g / n), uint8(b / n)}
}

// sextantOf is one cell from its visible samples.
func sextantOf(visible []bitSample) sextantCell {
	if len(visible) == 0 {
		return sextantCell{glyph: ' '}
	}
	colours := make([]rgb, len(visible))
	for i, s := range visible {
		colours[i] = s.c
	}

	// Samples outside the mask must stay transparent, so a partly covered cell can only
	// paint its visible samples in the foreground — there is no second colour to spend.
	if len(visible) < 6 {
		var mask uint8
		for _, s := range visible {
			mask |= 1 << s.bit
		}
		return sextantCell{glyph: sextant(mask), fg: mean(colours), hasFG: true}
	}

	lo, hi := float32(math.MaxFloat32), float32(-math.MaxFloat32)
	for _, c := range colours {
		lo, hi = min(lo, luma(c)), max(hi, luma(c))
	}
	// A flat cell has no split worth making; drawing it solid avoids turning sensor noise
	// into a dither pattern.
	if hi-lo < 8 {
		return sextantCell{glyph: '█', fg: mean(colours), hasFG: true}
	}

	mid := (lo + hi) / 2
	var fg, bg []rgb
	var mask uint8
	for _, s := range visible {
		if luma(s.c) >= mid {
			mask |= 1 << s.bit
			fg = append(fg, s.c)
		} else {
			bg = append(bg, s.c)
		}
	}
	return sextantCell{glyph: sextant(mask), fg: mean(fg), bg: mean(bg), hasFG: true, hasBG: true}
}

// sextant is the glyph for a 2x3 bitmask. U+1FB00..=U+1FB3B covers 60 of the 64
// combinations — the empty, full and two half-column cases already exist as block
// elements, and the code points skip them.
func sextant(mask uint8) rune {
	switch mask {
	case 0:
		return ' '
	case 0b010101:
		return '▌'
	case 0b101010:
		return '▐'
	case 0b111111:
		return '█'
	}
	index := rune(mask) - 1
	if mask > 0b010101 {
		index--
	}
	if mask > 0b101010 {
		index--
	}
	return 0x1FB00 + index
}

// sextantString renders cells as styled text, one line per row.
func sextantString(cells [][]sextantCell) string {
	var b strings.Builder
	for i, row := range cells {
		if i > 0 {
			b.WriteByte('\n')
		}
		for _, cell := range row {
			if !cell.hasFG {
				b.WriteRune(cell.glyph)
				continue
			}
			s := plain.Foreground(cell.fg.colour())
			if cell.hasBG {
				s = s.Background(cell.bg.colour())
			}
			b.WriteString(s.Render(string(cell.glyph)))
		}
	}
	return b.String()
}
