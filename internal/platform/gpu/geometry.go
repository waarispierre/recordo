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

// Zoom is the camera over the composited scene for one output frame.
//
// The scene is the canvas as it looks at rest: the window at its full size, padded on the
// background. The window never moves or resizes. Instead the camera magnifies the whole
// scene — background, shadow, window frame and content alike — by Scale around Centre, as
// if moving closer to the screen, so at higher zooms the window's frame passes out of
// shot.
type Zoom struct {
	// Scale is the magnification; 1 shows the whole scene.
	Scale float32
	// Centre is the scene point, in output pixels, that lands at the centre of the frame.
	Centre [2]float32
}

// ZoomFor derives a Zoom from a content-space zoom crop (see camera.Solve).
//
// The magnification is the crop's, so the content appears contentW/crop.W times its
// resting size. The centre is the crop's centre, mapped from content space to where that
// content sits in the scene, then held far enough from the canvas edges that the camera
// never looks past them. At rest the crop is the full frame, so the camera shows exactly
// the whole canvas.
//
// chromeH must be the title bar height the renderer draws (ChromeHeight of the source
// height), so the mapping matches the shader's layout.
func ZoomFor(crop camera.Crop, contentW, contentH float64, outW, outH uint32, padding, chromeH float32) Zoom {
	scale := math.Max(contentW/math.Max(crop.W, 1e-6), 1)
	ow, oh := float64(outW), float64(outH)
	pad, chrome := float64(padding), float64(chromeH)

	// The content area in the scene, laid out as the shader does: the window fills the
	// canvas inside the padding, and the content fills the window below the chrome.
	areaW, areaH := ow-2*pad, oh-2*pad-chrome
	cx := pad + (crop.X+crop.W/2)/contentW*areaW
	cy := pad + chrome + (crop.Y+crop.H/2)/contentH*areaH

	halfW, halfH := ow/(2*scale), oh/(2*scale)
	return Zoom{
		Scale:  float32(scale),
		Centre: [2]float32{float32(clamp64(cx, halfW, ow-halfW)), float32(clamp64(cy, halfH, oh-halfH))},
	}
}

func clamp64(v, lo, hi float64) float64 { return math.Max(lo, math.Min(v, hi)) }

func clamp32(v, lo, hi float32) float32 { return max(lo, min(v, hi)) }
