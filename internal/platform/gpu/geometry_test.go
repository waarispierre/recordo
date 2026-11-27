package gpu

import (
	"math"
	"testing"
	"unsafe"

	"github.com/waarispierre/recordo/internal/domain/camera"
	"github.com/waarispierre/recordo/internal/domain/config"
)

func TestUniformsLayoutMatchesTheShader(t *testing.T) {
	// Offsets follow WGSL's alignment rules for the Uniforms struct in shader.wgsl.
	var u uniforms
	for name, got := range map[string]uintptr{
		"ZoomCentre":   unsafe.Offsetof(u.ZoomCentre),
		"OutSize":      unsafe.Offsetof(u.OutSize),
		"BgTop":        unsafe.Offsetof(u.BgTop),
		"ChromeHeight": unsafe.Offsetof(u.ChromeHeight),
		"PillColor":    unsafe.Offsetof(u.PillColor),
		"CamRect":      unsafe.Offsetof(u.CamRect),
		"CamEnabled":   unsafe.Offsetof(u.CamEnabled),
	} {
		want := map[string]uintptr{
			"ZoomCentre": 16, "OutSize": 32, "BgTop": 64, "ChromeHeight": 112,
			"PillColor": 128, "CamRect": 160, "CamEnabled": 180,
		}[name]
		if got != want {
			t.Errorf("%s at offset %d, want %d", name, got, want)
		}
	}
	if s := unsafe.Sizeof(u); s != 192 {
		t.Errorf("uniforms is %d bytes, want 192", s)
	}
}

func TestOutputSizeIsEvenAndHoldsContentAtOneToOne(t *testing.T) {
	s := config.DefaultStyle()
	w, h := OutputSize(1511, 981, s)
	if w%2 != 0 || h%2 != 0 {
		t.Fatalf("odd output %dx%d", w, h)
	}
	if w < 1511+96 || h < 981+96+uint32(ChromeHeight(s.Chrome, 981)) {
		t.Fatalf("output %dx%d too small for content", w, h)
	}
}

func TestChromeHeightIsClamped(t *testing.T) {
	if got := ChromeHeight(config.ChromeWindow, 100); got != 28 {
		t.Errorf("small content: %v", got)
	}
	if got := ChromeHeight(config.ChromeBrowser, 10000); got != 76 {
		t.Errorf("large content: %v", got)
	}
	if got := ChromeHeight(config.ChromeNone, 1000); got != 0 {
		t.Errorf("no chrome: %v", got)
	}
}

// cornerZoom is the camera's crop at the given zoom, pushed into the bottom-right corner.
func cornerZoom(cw, ch, zoom float64) camera.Crop {
	return camera.Crop{X: cw - cw/zoom, Y: ch - ch/zoom, W: cw / zoom, H: ch / zoom}
}

const (
	testCW, testCH       = 1000.0, 600.0
	testOutW, testOutH   = 1100, 740
	testPad, testChromeH = float32(50), float32(40)
)

func TestCameraRestsOnTheWholeCanvas(t *testing.T) {
	z := ZoomFor(camera.Crop{W: testCW, H: testCH}, testCW, testCH, testOutW, testOutH, testPad, testChromeH)
	if z.Scale != 1 || z.Centre != [2]float32{testOutW / 2, testOutH / 2} {
		t.Fatalf("at rest: %+v", z)
	}
}

func TestZoomMagnifiesTheWholeSceneByTheCrop(t *testing.T) {
	// The regression: zoom used to resize the window inside a fixed frame, so the zoom
	// setting never brought the camera closer. Now the scene is magnified by the crop's zoom.
	for _, zoom := range []float64{1.2, 1.5, 3} {
		z := ZoomFor(cornerZoom(testCW, testCH, zoom), testCW, testCH, testOutW, testOutH, testPad, testChromeH)
		if math.Abs(float64(z.Scale)-zoom) > 1e-5 {
			t.Errorf("zoom %v: scale %v", zoom, z.Scale)
		}
	}
}

func TestCameraNeverLooksPastTheCanvas(t *testing.T) {
	for _, zoom := range []float64{1.2, 1.5, 3} {
		z := ZoomFor(cornerZoom(testCW, testCH, zoom), testCW, testCH, testOutW, testOutH, testPad, testChromeH)
		halfW, halfH := float32(testOutW)/2/z.Scale, float32(testOutH)/2/z.Scale
		if z.Centre[0]-halfW < -1e-3 || z.Centre[0]+halfW > testOutW+1e-3 ||
			z.Centre[1]-halfH < -1e-3 || z.Centre[1]+halfH > testOutH+1e-3 {
			t.Errorf("zoom %v: view %+v leaves the canvas", zoom, z)
		}
	}
}

func TestCameraCentresOnTheCropsContent(t *testing.T) {
	// A 2x crop around the middle of the content: the camera centres on where that content
	// sits in the scene, below the chrome rather than at the canvas centre.
	crop := camera.Crop{X: testCW / 4, Y: testCH / 4, W: testCW / 2, H: testCH / 2}
	z := ZoomFor(crop, testCW, testCH, testOutW, testOutH, testPad, testChromeH)
	areaH := float32(testOutH) - 2*testPad - testChromeH
	want := [2]float32{testOutW / 2, testPad + testChromeH + areaH/2}
	if math.Abs(float64(z.Centre[0]-want[0])) > 1e-3 || math.Abs(float64(z.Centre[1]-want[1])) > 1e-3 {
		t.Fatalf("centre %v, want %v", z.Centre, want)
	}
}
