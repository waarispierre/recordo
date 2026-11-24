package gpu

import (
	"math"

	"github.com/waarispierre/recordo/internal/domain/camera"
	"github.com/waarispierre/recordo/internal/domain/config"
)

// ChromeHeight is the title bar height for a given content height. Deriving it from the
// content rather than the output avoids a circular dependency, since the output height
// is itself content + chrome.
func ChromeHeight(c config.Chrome, contentH uint32) float32 {
	if c == config.ChromeNone {
		return 0
	}
	return clamp32(float32(contentH)*0.042, 28, 76)
}

func chromeStyle(c config.Chrome) float32 {
	if c == config.ChromeBrowser {
		return 1
	}
	return 0
}

// Scaled converts a style's point lengths to pixels for a capture recorded at scale.
//
// Without this the look changes with the capture scale: at 2x Retina a radius meant to
// match macOS's own ~12pt corners renders half-size and reads as a hard edge, and on
// native-app captures it fails to clip the window's own rounded corners, leaving dark
// crescents.
func Scaled(s config.Style, scale float32) config.Style {
	s.Padding *= scale
	s.CornerRadius *= scale
	s.ShadowOffset = [2]float32{s.ShadowOffset[0] * scale, s.ShadowOffset[1] * scale}
	s.ShadowBlur *= scale
	return s
}

// OutputSize is the output dimensions that hold the content at 1:1 plus chrome and
// padding. Both are rounded up to even numbers, which yuv420p encoding requires.
func OutputSize(srcW, srcH uint32, s config.Style) (uint32, uint32) {
	pad := uint32(math.Round(float64(s.Padding))) * 2
	chrome := uint32(math.Round(float64(ChromeHeight(s.Chrome, srcH))))
	return (srcW + pad + 1) &^ 1, (srcH + chrome + pad + 1) &^ 1
}

// WindowZoom is the whole-window scale and pan for one output frame: how much of its
// full, canvas-filling size the window (chrome, content, corners and shadow, as one unit)
// is drawn at, and how far its centre shifts from the canvas centre.
type WindowZoom struct {
	Scale  float32
	Offset [2]float32
}

// ZoomFor derives a WindowZoom from a content-space zoom crop (see camera.Solve).
//
// Scale is the crop's magnification relative to maxZoom, so the window is at its
// smallest, resting size when the crop is the full frame and grows to fill the canvas
// exactly as the crop reaches its tightest. The pan comes from how far the crop's centre
// has drifted from the content's centre — zero at rest, since an unzoomed crop is always
// the full frame — applied against whatever room is actually left on each axis once the
// window's own size is subtracted from the canvas. That room shrinks to a constant (the
// padding margin) as scale reaches 1.0, so the window can never grow past the canvas and
// its shadow and rounded corners are never clipped.
func ZoomFor(crop camera.Crop, contentW, contentH, maxZoom float64, outW, outH uint32, padding float32) WindowZoom {
	zoom := contentW / math.Max(crop.W, 1e-6)
	scale := clamp32(float32(zoom/math.Max(maxZoom, 1e-6)), 0, 1)

	// Signed fraction of content size the crop's centre has drifted from the content's own
	// centre; bounded to roughly +/-0.5 by the crop always fitting inside the content.
	fx := ((crop.X + crop.W/2) - contentW/2) / contentW
	fy := ((crop.Y + crop.H/2) - contentH/2) / contentH

	halfW, halfH := float32(outW)/2, float32(outH)/2
	winHalfW, winHalfH := (halfW-padding)*scale, (halfH-padding)*scale
	availW, availH := halfW-winHalfW, halfH-winHalfH

	return WindowZoom{
		Scale: scale,
		Offset: [2]float32{
			clamp32(float32(fx)*2, -1, 1) * availW,
			clamp32(float32(fy)*2, -1, 1) * availH,
		},
	}
}

func clamp32(v, lo, hi float32) float32 { return max(lo, min(v, hi)) }
