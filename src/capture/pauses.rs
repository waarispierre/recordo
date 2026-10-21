//! Pausing a recording.
//!
//! ScreenCaptureKit's recording output cannot be paused, and stopping and restarting it
//! would leave several files to stitch back together. So capture never actually stops:
//! the pauses are logged on the shared mach timebase and the render cuts them out of the
//! screen, the webcam and the voice over alike. The raw capture still holds what happened
//! while paused, the same way it still holds the browser chrome the render removes.

use crate::capture::clock;
use serde::{Deserialize, Serialize};

/// One paused span, in mach nanoseconds — the same timebase as frame display times.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
pub struct Pause {
    pub start_ns: u64,
    pub end_ns: u64,
}

/// The pauses taken during one recording, handed to the caller's `stop` callback.
#[derive(Debug, Default)]
pub struct PauseLog {
    done: Vec<Pause>,
    open: Option<u64>,
}

impl PauseLog {
    pub fn is_paused(&self) -> bool {
        self.open.is_some()
    }

    /// Pauses if running, resumes if paused. Returns whether it is now paused.
    pub fn toggle(&mut self) -> bool {
        let now = clock::now_nanos();
        match self.open.take() {
            Some(start_ns) => {
                self.done.push(Pause {
                    start_ns,
                    end_ns: now,
                });
                false
            }
            None => {
                self.open = Some(now);
                true
            }
        }
    }

    /// Total time spent paused so far, including a pause still in progress.
    pub fn paused_ns(&self) -> u64 {
        let closed: u64 = self.done.iter().map(|p| p.end_ns - p.start_ns).sum();
        closed
            + self
                .open
                .map_or(0, |s| clock::now_nanos().saturating_sub(s))
    }

    /// Every pause, closing one still open at `end_ns` — stopping while paused means the
    /// tail is not wanted either.
    pub fn finish(self, end_ns: u64) -> Vec<Pause> {
        let mut pauses = self.done;
        if let Some(start_ns) = self.open {
            pauses.push(Pause { start_ns, end_ns });
        }
        pauses
    }
}

/// True when `t_ns` falls inside any of `pauses`.
pub fn is_paused_at(pauses: &[Pause], t_ns: u64) -> bool {
    pauses.iter().any(|p| p.start_ns <= t_ns && t_ns < p.end_ns)
}

/// An ffmpeg `aselect` expression that drops the paused spans of an audio stream whose
/// t=0 sits at `base_ns`, or None when nothing needs cutting.
pub fn audio_select(pauses: &[Pause], base_ns: u64) -> Option<String> {
    let secs = |ns: u64| ns.saturating_sub(base_ns) as f64 / 1e9;
    let spans: Vec<String> = pauses
        .iter()
        .filter(|p| p.end_ns > base_ns)
        .map(|p| format!("between(t,{:.6},{:.6})", secs(p.start_ns), secs(p.end_ns)))
        .collect();
    (!spans.is_empty()).then(|| {
        // asetpts closes the gaps aselect leaves, or the encoder pads them with silence.
        format!("aselect='not({})',asetpts=N/SR/TB", spans.join("+"))
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_frame_inside_a_pause_is_cut_and_its_edges_are_half_open() {
        let pauses = [Pause {
            start_ns: 100,
            end_ns: 200,
        }];
        assert!(!is_paused_at(&pauses, 99));
        assert!(is_paused_at(&pauses, 100));
        assert!(is_paused_at(&pauses, 199));
        assert!(!is_paused_at(&pauses, 200));
    }

    #[test]
    fn stopping_while_paused_closes_the_pause_at_the_stop() {
        let log = PauseLog {
            done: vec![],
            open: Some(500),
        };
        assert_eq!(
            log.finish(900),
            vec![Pause {
                start_ns: 500,
                end_ns: 900
            }]
        );
    }

    #[test]
    fn audio_cuts_are_relative_to_the_first_frame() {
        let base = 10_000_000_000;
        let pauses = [Pause {
            start_ns: base + 2_000_000_000,
            end_ns: base + 3_500_000_000,
        }];
        assert_eq!(
            audio_select(&pauses, base).as_deref(),
            Some("aselect='not(between(t,2.000000,3.500000))',asetpts=N/SR/TB")
        );
        assert_eq!(audio_select(&[], base), None);
    }
}
