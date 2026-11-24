// Package camera is the virtual camera: telemetry in, one crop rectangle per output frame.
//
// This is the quality core of the product and deliberately has no OS or GPU dependency,
// so it can be tuned headlessly against recorded fixtures.
//
// Shape of the model:
//   - Clicks drive a zoom envelope — ramp in, hold, ramp out — combined by max so rapid
//     clicks extend a single zoom rather than pumping in and out.
//   - The cursor is followed by a critically damped spring, which converges without
//     overshoot. Tracking raw coordinates looks jittery and induces motion sickness.
//   - While zoomed out the camera returns to frame centre, so a still recording has no
//     drift.
package camera

import (
	"math"

	"github.com/waarispierre/recordo/internal/telemetry"
)

// Config tunes how the camera moves.
type Config struct {
	// MaxZoom is the magnification at full zoom.
	MaxZoom  float64
	ZoomInS  float64
	HoldS    float64
	ZoomOutS float64
	// FollowHz is the spring frequency — higher follows the cursor more tightly.
	FollowHz float64
	// FollowDeadzonePercent is the radius of the deadzone around the camera centre, as a
	// percentage of the visible width. The camera ignores cursor movement inside it.
	// 0 follows continuously.
	FollowDeadzonePercent float64
}

// DefaultConfig returns the tuned defaults.
func DefaultConfig() Config {
	return Config{
		MaxZoom:               1.45,
		ZoomInS:               0.45,
		HoldS:                 1.30,
		ZoomOutS:              0.70,
		FollowHz:              1.1,
		FollowDeadzonePercent: 10.0,
	}
}

// Crop is the source-pixel rectangle to sample for one output frame.
type Crop struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`
}

func smoothstep(t float64) float64 {
	t = clamp(t, 0, 1)
	return t * t * (3 - 2*t)
}

func clamp(v, lo, hi float64) float64 { return math.Max(lo, math.Min(v, hi)) }

// clickEnvelope is the zoom weight in [0,1] for a single click, dt seconds after it.
func clickEnvelope(dt float64, cfg *Config) float64 {
	if dt < 0 {
		return 0
	}
	rampEnd := cfg.ZoomInS
	holdEnd := rampEnd + cfg.HoldS
	outEnd := holdEnd + cfg.ZoomOutS
	switch {
	case dt < rampEnd:
		return smoothstep(dt / cfg.ZoomInS)
	case dt < holdEnd:
		return 1
	case dt < outEnd:
		return 1 - smoothstep((dt-holdEnd)/cfg.ZoomOutS)
	default:
		return 0
	}
}

func envelopeAt(clicks []uint64, tNs uint64, cfg *Config) float64 {
	best := 0.0
	for _, c := range clicks {
		// Clicks are sorted; once we are before a click every later one is too.
		if tNs < c {
			break
		}
		best = math.Max(best, clickEnvelope(float64(tNs-c)/1e9, cfg))
		if best >= 1 {
			return 1
		}
	}
	return best
}

type move struct {
	t    uint64
	x, y float64
}

// Solve returns a crop rectangle for every frame timestamp.
//
// frameTNs must be ascending mach-nanosecond display times, the same timebase the
// telemetry uses.
func Solve(tel *telemetry.Telemetry, frameTNs []uint64, width, height float64, cfg Config) []Crop {
	var moves []move
	var clicks []uint64
	for _, e := range tel.Events {
		switch e.Kind {
		case telemetry.Move:
			moves = append(moves, move{e.TNs, e.X, e.Y})
		case telemetry.Down:
			clicks = append(clicks, e.TNs)
		}
	}

	cx, cy := width/2, height/2
	var vx, vy float64
	omega := 2 * math.Pi * cfg.FollowHz

	idx := 0
	var lastT uint64
	if len(frameTNs) > 0 {
		lastT = frameTNs[0]
	}
	out := make([]Crop, 0, len(frameTNs))

	for _, t := range frameTNs {
		// Hold the most recent sample at or before this frame. Sampling is deduplicated
		// while the cursor is parked, so gaps mean "stationary", not "missing" — never
		// interpolate across them.
		for idx+1 < len(moves) && moves[idx+1].t <= t {
			idx++
		}
		tx, ty := width/2, height/2
		if idx < len(moves) {
			tx, ty = moves[idx].x, moves[idx].y
		}

		// Clamp dt so a long stall cannot blow up the integrator.
		dt := clamp((float64(t)-float64(lastT))/1e9, 0, 0.1)
		lastT = t

		// Zoom is resolved before the follow so the deadzone can be sized against the
		// visible area rather than the source. Scaled that way, the boundary covers the
		// same portion of the exported frame at every zoom level, which is what makes it
		// feel consistent.
		env := envelopeAt(clicks, t, &cfg)
		zoom := 1 + (cfg.MaxZoom-1)*env
		cw, ch := width/zoom, height/zoom

		// Deadzone: the camera ignores the cursor until it leaves a circle of this radius
		// around the current centre, then moves only far enough to put it back on the
		// boundary. Following the cursor exactly makes the camera twitch at every small
		// movement; trailing by the radius keeps it still during ordinary pointing and
		// still tracks a deliberate traverse.
		deadzone := cw * (math.Max(cfg.FollowDeadzonePercent, 0) / 100)
		dx, dy := tx-cx, ty-cy
		distance := math.Hypot(dx, dy)
		goalX, goalY := cx, cy
		if distance > deadzone && distance != 0 {
			pull := (distance - deadzone) / distance
			goalX, goalY = cx+dx*pull, cy+dy*pull
		}

		// Semi-implicit Euler on a critically damped spring.
		vx += (-2*omega*vx - omega*omega*(cx-goalX)) * dt
		vy += (-2*omega*vy - omega*omega*(cy-goalY)) * dt
		cx += vx * dt
		cy += vy * dt

		// Blend toward frame centre as we zoom out, so an idle recording sits still.
		ccx := width/2 + (cx-width/2)*env
		ccy := height/2 + (cy-height/2)*env

		out = append(out, Crop{
			X: clamp(ccx-cw/2, 0, math.Max(width-cw, 0)),
			Y: clamp(ccy-ch/2, 0, math.Max(height-ch, 0)),
			W: cw,
			H: ch,
		})
	}
	return out
}
