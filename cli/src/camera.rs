//! Virtual camera: telemetry -> a crop rectangle per output frame.
//!
//! This is the quality core of the product and deliberately has no OS or GPU
//! dependency, so it can be tuned headlessly against recorded fixtures.
//!
//! Shape of the model:
//!   * Clicks drive a zoom *envelope* — ramp in, hold, ramp out — combined by max so
//!     rapid clicks extend a single zoom rather than pumping in and out.
//!   * The cursor is followed by a critically damped spring, which converges without
//!     overshoot. Tracking raw coordinates looks jittery and induces motion sickness.
//!   * While zoomed out the camera returns to frame centre, so a still recording has no
//!     drift.

use crate::telemetry::{EventKind, Telemetry};
use serde::Serialize;

#[derive(Debug, Clone, Copy)]
pub struct CameraConfig {
    /// Magnification at full zoom.
    pub max_zoom: f64,
    pub zoom_in_s: f64,
    pub hold_s: f64,
    pub zoom_out_s: f64,
    /// Spring frequency in Hz — higher follows the cursor more tightly.
    pub follow_hz: f64,
}

impl Default for CameraConfig {
    fn default() -> Self {
        Self {
            max_zoom: 1.45,
            zoom_in_s: 0.45,
            hold_s: 1.30,
            zoom_out_s: 0.70,
            follow_hz: 1.1,
        }
    }
}

/// Source-pixel rectangle to sample for one output frame.
#[derive(Debug, Clone, Copy, PartialEq, Serialize)]
pub struct Crop {
    pub x: f64,
    pub y: f64,
    pub w: f64,
    pub h: f64,
}

fn smoothstep(t: f64) -> f64 {
    let t = t.clamp(0.0, 1.0);
    t * t * (3.0 - 2.0 * t)
}

/// Zoom weight in [0,1] for a single click, `dt` seconds after it happened.
fn click_envelope(dt: f64, cfg: &CameraConfig) -> f64 {
    if dt < 0.0 {
        return 0.0;
    }
    let ramp_end = cfg.zoom_in_s;
    let hold_end = ramp_end + cfg.hold_s;
    let out_end = hold_end + cfg.zoom_out_s;

    if dt < ramp_end {
        smoothstep(dt / cfg.zoom_in_s)
    } else if dt < hold_end {
        1.0
    } else if dt < out_end {
        1.0 - smoothstep((dt - hold_end) / cfg.zoom_out_s)
    } else {
        0.0
    }
}

fn envelope_at(clicks: &[u64], t_ns: u64, cfg: &CameraConfig) -> f64 {
    let mut best: f64 = 0.0;
    for &c in clicks {
        let dt = (t_ns as i128 - c as i128) as f64 / 1e9;
        // Clicks are sorted; once we are before a click every later one is too.
        if dt < 0.0 {
            break;
        }
        best = best.max(click_envelope(dt, cfg));
        if best >= 1.0 {
            return 1.0;
        }
    }
    best
}

/// Solves a crop rectangle for every frame timestamp.
///
/// `frame_t_ns` must be ascending mach-nanosecond display times, the same timebase the
/// telemetry uses.
pub fn solve(
    tel: &Telemetry,
    frame_t_ns: &[u64],
    width: f64,
    height: f64,
    cfg: &CameraConfig,
) -> Vec<Crop> {
    let moves: Vec<(u64, f64, f64)> = tel
        .events
        .iter()
        .filter(|e| e.kind == EventKind::Move)
        .map(|e| (e.t_ns, e.x, e.y))
        .collect();
    let clicks: Vec<u64> = tel
        .events
        .iter()
        .filter(|e| e.kind == EventKind::Down)
        .map(|e| e.t_ns)
        .collect();

    let (mut cx, mut cy) = (width / 2.0, height / 2.0);
    let (mut vx, mut vy) = (0.0f64, 0.0f64);
    let omega = std::f64::consts::TAU * cfg.follow_hz;

    let mut idx = 0usize;
    let mut last_t = frame_t_ns.first().copied().unwrap_or(0);
    let mut out = Vec::with_capacity(frame_t_ns.len());

    for &t in frame_t_ns {
        // Hold the most recent sample at or before this frame. Sampling is deduplicated
        // while the cursor is parked, so gaps mean "stationary", not "missing" — never
        // interpolate across them.
        while idx + 1 < moves.len() && moves[idx + 1].0 <= t {
            idx += 1;
        }
        let (tx, ty) = match moves.get(idx) {
            Some(&(_, x, y)) => (x, y),
            None => (width / 2.0, height / 2.0),
        };

        // Clamp dt so a long stall cannot blow up the integrator.
        let dt = ((t as i128 - last_t as i128) as f64 / 1e9).clamp(0.0, 0.1);
        last_t = t;

        // Semi-implicit Euler on a critically damped spring.
        vx += (-2.0 * omega * vx - omega * omega * (cx - tx)) * dt;
        vy += (-2.0 * omega * vy - omega * omega * (cy - ty)) * dt;
        cx += vx * dt;
        cy += vy * dt;

        let env = envelope_at(&clicks, t, cfg);
        let zoom = 1.0 + (cfg.max_zoom - 1.0) * env;
        let (cw, ch) = (width / zoom, height / zoom);

        // Blend toward frame centre as we zoom out, so an idle recording sits still.
        let ccx = width / 2.0 + (cx - width / 2.0) * env;
        let ccy = height / 2.0 + (cy - height / 2.0) * env;

        out.push(Crop {
            x: (ccx - cw / 2.0).clamp(0.0, (width - cw).max(0.0)),
            y: (ccy - ch / 2.0).clamp(0.0, (height - ch).max(0.0)),
            w: cw,
            h: ch,
        });
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::telemetry::TelemetryEvent;

    const W: f64 = 1512.0;
    const H: f64 = 982.0;

    fn frames(n: usize, fps: u64) -> Vec<u64> {
        (0..n as u64).map(|i| i * 1_000_000_000 / fps).collect()
    }

    fn tel(events: Vec<TelemetryEvent>) -> Telemetry {
        Telemetry {
            events,
            tap_installed: true,
        }
    }

    fn ev(t_ms: u64, kind: EventKind, x: f64, y: f64) -> TelemetryEvent {
        TelemetryEvent {
            t_ns: t_ms * 1_000_000,
            kind,
            x,
            y,
        }
    }

    #[test]
    fn empty_telemetry_yields_full_frame() {
        let crops = solve(
            &tel(vec![]),
            &frames(60, 60),
            W,
            H,
            &CameraConfig::default(),
        );
        assert_eq!(crops.len(), 60);
        for c in crops {
            assert_eq!(
                c,
                Crop {
                    x: 0.0,
                    y: 0.0,
                    w: W,
                    h: H
                }
            );
        }
    }

    #[test]
    fn crop_always_within_source_bounds() {
        // Cursor pinned to a corner is the case most likely to push the crop off-frame.
        let mut events = vec![ev(0, EventKind::Down, 0.0, 0.0)];
        for i in 0..200 {
            events.push(ev(i * 10, EventKind::Move, 0.0, 0.0));
        }
        let crops = solve(
            &tel(events),
            &frames(180, 60),
            W,
            H,
            &CameraConfig::default(),
        );
        for c in crops {
            assert!(c.x >= -1e-9 && c.y >= -1e-9, "negative origin: {c:?}");
            assert!(c.x + c.w <= W + 1e-9, "overflows right: {c:?}");
            assert!(c.y + c.h <= H + 1e-9, "overflows bottom: {c:?}");
            assert!(c.w > 0.0 && c.h > 0.0);
            assert!(c.w.is_finite() && c.h.is_finite());
        }
    }

    #[test]
    fn click_zooms_in_then_returns_to_full_frame() {
        let cfg = CameraConfig::default();
        let events = vec![
            ev(0, EventKind::Move, W / 2.0, H / 2.0),
            ev(500, EventKind::Down, W / 2.0, H / 2.0),
        ];
        let crops = solve(&tel(events), &frames(300, 60), W, H, &cfg);

        assert!((crops[0].w - W).abs() < 1e-6, "should start unzoomed");

        let tightest = crops.iter().map(|c| c.w).fold(f64::MAX, f64::min);
        let expected = W / cfg.max_zoom;
        assert!(
            (tightest - expected).abs() < 1.0,
            "expected to reach {expected:.1}px wide, got {tightest:.1}"
        );

        // 0.5s click + full envelope (0.45 + 1.30 + 0.70 = 2.45s) lands before frame 300
        // at 60fps (5.0s), so the camera must have returned to the full frame.
        assert!(
            (crops.last().unwrap().w - W).abs() < 1.0,
            "should return to full frame, got {:?}",
            crops.last()
        );
    }

    #[test]
    fn motion_is_continuous() {
        // Teleporting the cursor must not teleport the camera — the spring is what
        // keeps exports from looking nauseating.
        let mut events = vec![ev(0, EventKind::Down, 100.0, 100.0)];
        for i in 0..100 {
            let far = if i % 2 == 0 { 50.0 } else { W - 50.0 };
            events.push(ev(i * 10, EventKind::Move, far, H / 2.0));
        }
        let crops = solve(
            &tel(events),
            &frames(120, 60),
            W,
            H,
            &CameraConfig::default(),
        );

        let max_step = crops
            .windows(2)
            .map(|w| (w[1].x - w[0].x).abs().max((w[1].y - w[0].y).abs()))
            .fold(0.0f64, f64::max);
        assert!(
            max_step < 40.0,
            "camera jumped {max_step:.1}px in one frame"
        );
    }

    #[test]
    fn rapid_clicks_extend_one_zoom_rather_than_pumping() {
        let cfg = CameraConfig::default();
        let mut events = vec![ev(0, EventKind::Move, W / 2.0, H / 2.0)];
        for i in 0..5 {
            events.push(ev(i * 300, EventKind::Down, W / 2.0, H / 2.0));
        }
        events.sort_by_key(|e| e.t_ns);
        let crops = solve(&tel(events), &frames(180, 60), W, H, &cfg);

        // Once fully zoomed the width must never widen again until the final ramp out.
        let min_w = W / cfg.max_zoom;
        let first_full = crops.iter().position(|c| (c.w - min_w).abs() < 1.0);
        assert!(first_full.is_some(), "never reached full zoom");
        let start = first_full.unwrap();
        let last_full = crops
            .iter()
            .rposition(|c| (c.w - min_w).abs() < 1.0)
            .unwrap();
        for c in &crops[start..=last_full] {
            assert!(
                (c.w - min_w).abs() < 1.0,
                "zoom pumped back out mid-sequence: {c:?}"
            );
        }
    }
}
