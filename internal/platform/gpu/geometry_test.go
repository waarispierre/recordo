package gpu

import (
	"bytes"
	"math"
	"os"
	"testing"
	"unsafe"

	"github.com/waarispierre/recordo/internal/domain/camera"
	"github.com/waarispierre/recordo/internal/domain/config"
)

func TestUniformsLayoutMatchesTheShader(t *testing.T) {
	// Offsets follow WGSL's alignment rules for the Uniforms struct in shader.wgsl.
	var u uniforms
	for name, got := range map[string]uintptr{
		"WinOffset":    unsafe.Offsetof(u.WinOffset),
		"OutSize":      unsafe.Offsetof(u.OutSize),
		"BgTop":        unsafe.Offsetof(u.BgTop),
		"ChromeHeight": unsafe.Offsetof(u.ChromeHeight),
		"PillColor":    unsafe.Offsetof(u.PillColor),
		"CamRect":      unsafe.Offsetof(u.CamRect),
		"CamEnabled":   unsafe.Offsetof(u.CamEnabled),
	} {
		want := map[string]uintptr{
			"WinOffset": 16, "OutSize": 32, "BgTop": 64, "ChromeHeight": 112,
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

func TestShaderMatchesTheRustCopy(t *testing.T) {
	// Until the Rust code is deleted, both copies of the shader must stay identical.
	rust, err := os.ReadFile("../../../src/render/shader.wgsl")
	if os.IsNotExist(err) {
		t.Skip("Rust shader gone")
	}
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(rust, []byte(shaderSource)) {
		t.Fatal("internal/render/shader.wgsl has drifted from src/render/shader.wgsl")
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

func TestWindowRestsAtFullFrameAndNeverLeavesTheCanvas(t *testing.T) {
	const cw, ch, maxZoom = 1000.0, 600.0, 1.5
	const outW, outH, pad = 1100, 700, 50
	rest := ZoomFor(camera.Crop{W: cw, H: ch}, cw, ch, maxZoom, outW, outH, pad)
	if rest.Offset != [2]float32{} || math.Abs(float64(rest.Scale)-1/maxZoom) > 1e-6 {
		t.Fatalf("at rest: %+v", rest)
	}
	// Fully zoomed into the bottom-right corner.
	z := ZoomFor(camera.Crop{X: cw - cw/maxZoom, Y: ch - ch/maxZoom, W: cw / maxZoom, H: ch / maxZoom},
		cw, ch, maxZoom, outW, outH, pad)
	if z.Scale != 1 {
		t.Fatalf("full zoom scale %v", z.Scale)
	}
	halfW := (outW/2 - pad) * z.Scale
	if right := outW/2 + z.Offset[0] + halfW; right > outW+1e-3 {
		t.Fatalf("window runs off the canvas: right edge %v", right)
	}
}
