package camera

import (
	"cmp"
	"math"
	"slices"
	"testing"

	"github.com/waarispierre/recordo/internal/telemetry"
)

const (
	W = 1512.0
	H = 982.0
)

func frames(n int, fps uint64) []uint64 {
	out := make([]uint64, n)
	for i := range out {
		out[i] = uint64(i) * 1_000_000_000 / fps
	}
	return out
}

func tel(events []telemetry.Event) *telemetry.Telemetry {
	slices.SortStableFunc(events, func(a, b telemetry.Event) int { return cmp.Compare(a.TNs, b.TNs) })
	return &telemetry.Telemetry{Events: events, TapInstalled: true}
}

func ev(tMs uint64, kind telemetry.EventKind, x, y float64) telemetry.Event {
	return telemetry.Event{TNs: tMs * 1_000_000, Kind: kind, X: x, Y: y}
}

// clicksEvery300ms holds the zoom envelope at full, so what a test measures is the
// follow behaviour rather than the camera recentring as the zoom decays.
func clicksEvery300ms() []telemetry.Event {
	var out []telemetry.Event
	for i := range uint64(10) {
		out = append(out, ev(i*300, telemetry.Down, W/2, H/2))
	}
	return out
}

func xSpread(crops []Crop) float64 {
	lo, hi := math.MaxFloat64, -math.MaxFloat64
	for _, c := range crops {
		lo, hi = math.Min(lo, c.X), math.Max(hi, c.X)
	}
	return hi - lo
}

func TestEmptyTelemetryYieldsFullFrame(t *testing.T) {
	crops := Solve(tel(nil), frames(60, 60), W, H, DefaultConfig())
	if len(crops) != 60 {
		t.Fatalf("got %d crops", len(crops))
	}
	for _, c := range crops {
		if c != (Crop{0, 0, W, H}) {
			t.Fatalf("expected full frame, got %+v", c)
		}
	}
}

func TestCropAlwaysWithinSourceBounds(t *testing.T) {
	// Cursor pinned to a corner is the case most likely to push the crop off-frame.
	events := []telemetry.Event{ev(0, telemetry.Down, 0, 0)}
	for i := range uint64(200) {
		events = append(events, ev(i*10, telemetry.Move, 0, 0))
	}
	for _, c := range Solve(tel(events), frames(180, 60), W, H, DefaultConfig()) {
		if c.X < -1e-9 || c.Y < -1e-9 || c.X+c.W > W+1e-9 || c.Y+c.H > H+1e-9 {
			t.Fatalf("crop out of bounds: %+v", c)
		}
		if c.W <= 0 || c.H <= 0 || math.IsInf(c.W, 0) || math.IsNaN(c.W) {
			t.Fatalf("degenerate crop: %+v", c)
		}
	}
}

func TestClickZoomsInThenReturnsToFullFrame(t *testing.T) {
	cfg := DefaultConfig()
	events := []telemetry.Event{
		ev(0, telemetry.Move, W/2, H/2),
		ev(500, telemetry.Down, W/2, H/2),
	}
	crops := Solve(tel(events), frames(300, 60), W, H, cfg)

	if math.Abs(crops[0].W-W) > 1e-6 {
		t.Fatal("should start unzoomed")
	}
	tightest := math.MaxFloat64
	for _, c := range crops {
		tightest = math.Min(tightest, c.W)
	}
	if want := W / cfg.MaxZoom; math.Abs(tightest-want) > 1 {
		t.Fatalf("expected to reach %.1fpx wide, got %.1f", want, tightest)
	}
	// 0.5s click + full envelope (0.45 + 1.30 + 0.70 = 2.45s) lands before frame 300 at
	// 60fps (5.0s), so the camera must have returned to the full frame.
	if last := crops[len(crops)-1]; math.Abs(last.W-W) > 1 {
		t.Fatalf("should return to full frame, got %+v", last)
	}
}

func TestDeadzoneHoldsTheCameraForSmallMovements(t *testing.T) {
	cfg := DefaultConfig()
	cfg.FollowDeadzonePercent = 10
	events := clicksEvery300ms()
	// Jitter well inside the boundary: 10% of a 1043px crop is ~104px radius.
	for i := range uint64(200) {
		nudge := 30.0
		if i%2 == 0 {
			nudge = -30
		}
		events = append(events, ev(i*10, telemetry.Move, W/2+nudge, H/2))
	}
	crops := Solve(tel(events), frames(180, 60), W, H, cfg)
	if d := xSpread(crops[90:]); d >= 1 {
		t.Fatalf("camera drifted %.1fpx for movement inside the deadzone", d)
	}
}

func TestDeadzoneStillFollowsADeliberateTraverse(t *testing.T) {
	cfg := DefaultConfig()
	cfg.FollowDeadzonePercent = 10
	events := clicksEvery300ms()
	// Walk the cursor far across the frame, well beyond any plausible boundary.
	for i := range uint64(150) {
		x := math.Min(W/2+float64(i)*3, W-10)
		events = append(events, ev(i*10, telemetry.Move, x, H/2))
	}
	crops := Solve(tel(events), frames(180, 60), W, H, cfg)
	if travelled := crops[len(crops)-1].X - crops[0].X; travelled <= 100 {
		t.Fatalf("camera should track a deliberate traverse, moved only %.1fpx", travelled)
	}
}

func TestZeroDeadzoneFollowsContinuously(t *testing.T) {
	// The escape hatch has to actually disable the feature.
	cfg := DefaultConfig()
	cfg.FollowDeadzonePercent = 0
	events := clicksEvery300ms()
	for i := range uint64(200) {
		nudge := 30.0
		if i%2 == 0 {
			nudge = -30
		}
		events = append(events, ev(i*10, telemetry.Move, W/2+nudge, H/2))
	}
	crops := Solve(tel(events), frames(180, 60), W, H, cfg)
	if d := xSpread(crops[90:]); d <= 1 {
		t.Fatalf("without a deadzone the camera should chase the jitter, moved %.2fpx", d)
	}
}

func TestMotionIsContinuous(t *testing.T) {
	// Teleporting the cursor must not teleport the camera — the spring is what keeps
	// exports from looking nauseating.
	events := []telemetry.Event{ev(0, telemetry.Down, 100, 100)}
	for i := range uint64(100) {
		far := W - 50
		if i%2 == 0 {
			far = 50
		}
		events = append(events, ev(i*10, telemetry.Move, far, H/2))
	}
	crops := Solve(tel(events), frames(120, 60), W, H, DefaultConfig())
	maxStep := 0.0
	for i := 1; i < len(crops); i++ {
		maxStep = math.Max(maxStep, math.Max(
			math.Abs(crops[i].X-crops[i-1].X), math.Abs(crops[i].Y-crops[i-1].Y)))
	}
	if maxStep >= 40 {
		t.Fatalf("camera jumped %.1fpx in one frame", maxStep)
	}
}

func TestRapidClicksExtendOneZoomRatherThanPumping(t *testing.T) {
	cfg := DefaultConfig()
	events := []telemetry.Event{ev(0, telemetry.Move, W/2, H/2)}
	for i := range uint64(5) {
		events = append(events, ev(i*300, telemetry.Down, W/2, H/2))
	}
	crops := Solve(tel(events), frames(180, 60), W, H, cfg)

	// Once fully zoomed the width must never widen again until the final ramp out.
	minW := W / cfg.MaxZoom
	full := func(c Crop) bool { return math.Abs(c.W-minW) < 1 }
	first := slices.IndexFunc(crops, full)
	if first < 0 {
		t.Fatal("never reached full zoom")
	}
	last := len(crops) - 1
	for !full(crops[last]) {
		last--
	}
	for _, c := range crops[first : last+1] {
		if !full(c) {
			t.Fatalf("zoom pumped back out mid-sequence: %+v", c)
		}
	}
}
