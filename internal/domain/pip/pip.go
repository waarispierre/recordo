// Package pip places the webcam picture-in-picture: corner + inset + size give an
// output-pixel rectangle, and a frame offset lines the camera's own timeline up with the
// screen's.
//
// Pure geometry, no OS or GPU dependency, so it is unit-tested the way camera is.
package pip

import "strings"

// Rect is a rectangle in output pixels.
type Rect struct{ X, Y, W, H float32 }

// Corner is which corner of the frame the webcam sits in.
type Corner int

const (
	BottomLeft Corner = iota
	BottomRight
	TopLeft
	TopRight
)

// ParseCorner reads a corner name case-insensitively, defaulting to BottomLeft.
func ParseCorner(s string) Corner {
	switch strings.ToLower(s) {
	case "bottom-right":
		return BottomRight
	case "top-left":
		return TopLeft
	case "top-right":
		return TopRight
	default:
		return BottomLeft
	}
}

func clamp(v, lo, hi float32) float32 { return max(lo, min(v, hi)) }

// PlaceRect returns where the webcam sits and how big it is, in output pixels.
//
// sizePercent sets the height; aspect (camera width / height, 1.0 for a circle, since that
// is cropped to a square before it reaches the renderer) derives the width from it, so the
// box never distorts the camera's own picture. The position is clamped to stay inside the
// frame — a large inset moves the box toward the opposite edge rather than off it, the
// same way a window manager would.
func PlaceRect(outW, outH float32, corner Corner, inset, offsetX, offsetY, sizePercent, aspect float32) Rect {
	h := clamp(outH*(max(sizePercent, 0)/100), 1, outH)
	w := clamp(h*max(aspect, 0.01), 1, outW)
	inset = max(inset, 0)

	var rawX, rawY float32
	switch corner {
	case BottomLeft:
		rawX, rawY = inset, outH-inset-h
	case BottomRight:
		rawX, rawY = outW-inset-w, outH-inset-h
	case TopLeft:
		rawX, rawY = inset, inset
	case TopRight:
		rawX, rawY = outW-inset-w, inset
	}

	return Rect{
		X: clamp(rawX+offsetX, 0, max(outW-w, 0)),
		Y: clamp(rawY+offsetY, 0, max(outH-h, 0)),
		W: w,
		H: h,
	}
}

// FrameOffset is how many output frames the camera's first sample sits away from the
// screen's.
//
// Positive: the camera started this many frames after the screen — the overlay should
// stay hidden for that many frames at the start. Negative: the camera started first, so
// that many of its frames should be read and discarded before compositing begins. Both
// streams are on the same host clock, so this is exact rather than an estimate.
func FrameOffset(screenBaseNs, cameraFirstNs, fps uint64) int64 {
	delta := int64(cameraFirstNs) - int64(screenBaseNs)
	frame := int64(1_000_000_000 / fps)
	// Floor division, matching Rust's div_euclid for a positive divisor.
	q := delta / frame
	if delta%frame < 0 {
		q--
	}
	return q
}
