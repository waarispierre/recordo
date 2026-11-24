package pip

import "testing"

const (
	outW = 1920
	outH = 1080
)

func inside(r Rect) bool { return r.X >= 0 && r.Y >= 0 && r.X+r.W <= outW && r.Y+r.H <= outH }

func TestEachCornerLandsInsideTheFrame(t *testing.T) {
	for _, c := range []Corner{BottomLeft, BottomRight, TopLeft, TopRight} {
		if r := PlaceRect(outW, outH, c, 32, 0, 0, 18, 1); !inside(r) {
			t.Errorf("corner %d spills past the frame: %+v", c, r)
		}
	}
}

func TestAHugeInsetIsClampedRatherThanPushedOffFrame(t *testing.T) {
	if r := PlaceRect(outW, outH, BottomLeft, 5000, 0, 0, 18, 1); !inside(r) {
		t.Fatalf("%+v", r)
	}
}

func TestAWideOffsetIsClampedRatherThanPushedOffFrame(t *testing.T) {
	if r := PlaceRect(outW, outH, TopRight, 32, -9000, 9000, 18, 1); !inside(r) {
		t.Fatalf("%+v", r)
	}
}

func TestRectWidthFollowsAspectNotJustHeight(t *testing.T) {
	square := PlaceRect(outW, outH, BottomLeft, 32, 0, 0, 18, 1)
	wide := PlaceRect(outW, outH, BottomLeft, 32, 0, 0, 18, 16.0/9.0)
	if square.H != wide.H || wide.W <= square.W {
		t.Fatalf("square %+v, wide %+v", square, wide)
	}
}

func TestFrameOffset(t *testing.T) {
	const frame = 1_000_000_000 / 60
	tests := []struct {
		name           string
		screen, camera uint64
		want           int64
	}{
		{"zero offset is the identity", 1_000_000_000, 1_000_000_000, 0},
		{"camera later", 1_000_000_000, 1_000_000_000 + frame*5, 5},
		{"camera earlier", 1_000_000_000 + frame*5, 1_000_000_000, -5},
		{"partial frame earlier floors away from zero", 1_000_000_000 + 10, 1_000_000_000, -1},
	}
	for _, tt := range tests {
		if got := FrameOffset(tt.screen, tt.camera, 60); got != tt.want {
			t.Errorf("%s: got %d, want %d", tt.name, got, tt.want)
		}
	}
}

func TestCornerParsesCaseInsensitivelyAndDefaultsToBottomLeft(t *testing.T) {
	for in, want := range map[string]Corner{
		"Bottom-Right": BottomRight, "top-left": TopLeft, "top-right": TopRight, "nonsense": BottomLeft,
	} {
		if got := ParseCorner(in); got != want {
			t.Errorf("ParseCorner(%q) = %d, want %d", in, got, want)
		}
	}
}
