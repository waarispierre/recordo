//! Cursor + click capture.
//!
//! ScreenCaptureKit exposes no per-frame cursor metadata, so cursor state is captured
//! out-of-band here and correlated with frames by mach timestamp.
//!
//! Two sources, deliberately:
//!   * Position is **polled** at a fixed rate. This needs no TCC permission, yields
//!     evenly-spaced samples (much easier for the camera model to smooth than irregular
//!     event arrivals), and keeps producing samples while the mouse is stationary.
//!   * Clicks come from a **CGEventTap**, which is the only way to see them, and which
//!     requires Input Monitoring permission.
//!
//! The tap failing therefore degrades the recording to "pan but never click-zoom"
//! rather than losing cursor tracking entirely.

use crate::capture::clock;
use core_foundation::runloop::{CFRunLoop, kCFRunLoopCommonModes, kCFRunLoopDefaultMode};
use core_graphics::event::{
    CGEvent, CGEventTap, CGEventTapLocation, CGEventTapOptions, CGEventTapPlacement, CGEventType,
    CallbackResult,
};
use core_graphics::event_source::{CGEventSource, CGEventSourceStateID};
use foreign_types::ForeignType;
use serde::{Deserialize, Serialize};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex};

pub const POLL_HZ: u64 = 120;

#[derive(Debug, Clone, Copy, Serialize, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "snake_case")]
pub enum EventKind {
    Move,
    Down,
    Up,
    Scroll,
}

#[derive(Debug, Clone, Copy, Serialize, Deserialize)]
pub struct TelemetryEvent {
    /// Mach absolute time in nanoseconds — same timebase as frame display_time.
    pub t_ns: u64,
    pub kind: EventKind,
    /// Global display coordinates, top-left origin.
    pub x: f64,
    pub y: f64,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Telemetry {
    pub events: Vec<TelemetryEvent>,
    /// Whether the event tap installed. False means Input Monitoring was denied and no
    /// clicks could have been seen; true with zero clicks simply means none happened.
    pub tap_installed: bool,
}

impl Telemetry {
    pub fn count(&self, kind: EventKind) -> usize {
        self.events.iter().filter(|e| e.kind == kind).count()
    }
}

// Not exposed by core-graphics 0.25, but it is the event's own hardware timestamp —
// more accurate than reading the clock whenever our callback happens to run.
unsafe extern "C" {
    fn CGEventGetTimestamp(event: core_graphics::sys::CGEventRef) -> u64;
}

fn event_timestamp_ns(event: &CGEvent) -> u64 {
    let raw = unsafe { CGEventGetTimestamp(event.as_ptr()) };
    if raw == 0 {
        clock::now_nanos()
    } else {
        clock::mach_to_nanos(raw)
    }
}

/// Reads the current cursor position without needing any permission.
fn cursor_position() -> Option<(f64, f64)> {
    let source = CGEventSource::new(CGEventSourceStateID::CombinedSessionState).ok()?;
    let event = CGEvent::new(source).ok()?;
    let p = event.location();
    Some((p.x, p.y))
}

pub struct TelemetryRecorder {
    events: Arc<Mutex<Vec<TelemetryEvent>>>,
    tap_installed: Arc<AtomicBool>,
    stop: Arc<AtomicBool>,
    poll_handle: Option<std::thread::JoinHandle<()>>,
    tap_handle: Option<std::thread::JoinHandle<()>>,
}

impl TelemetryRecorder {
    pub fn start() -> Self {
        let events = Arc::new(Mutex::new(Vec::new()));
        let tap_installed = Arc::new(AtomicBool::new(false));
        let stop = Arc::new(AtomicBool::new(false));

        let poll_handle = Some(Self::spawn_poller(Arc::clone(&events), Arc::clone(&stop)));
        let tap_handle = Some(Self::spawn_tap(
            Arc::clone(&events),
            Arc::clone(&stop),
            Arc::clone(&tap_installed),
        ));

        Self {
            events,
            tap_installed,
            stop,
            poll_handle,
            tap_handle,
        }
    }

    fn spawn_poller(
        events: Arc<Mutex<Vec<TelemetryEvent>>>,
        stop: Arc<AtomicBool>,
    ) -> std::thread::JoinHandle<()> {
        std::thread::spawn(move || {
            let interval = std::time::Duration::from_nanos(1_000_000_000 / POLL_HZ);
            let mut last: Option<(f64, f64)> = None;
            while !stop.load(Ordering::Relaxed) {
                if let Some((x, y)) = cursor_position() {
                    // Skip duplicate samples while the cursor is parked; the camera
                    // model can hold position from the previous sample, and this keeps
                    // idle recordings from bloating the sidecar.
                    if last != Some((x, y)) {
                        last = Some((x, y));
                        if let Ok(mut guard) = events.lock() {
                            guard.push(TelemetryEvent {
                                t_ns: clock::now_nanos(),
                                kind: EventKind::Move,
                                x,
                                y,
                            });
                        }
                    }
                }
                std::thread::sleep(interval);
            }
        })
    }

    fn spawn_tap(
        events: Arc<Mutex<Vec<TelemetryEvent>>>,
        stop: Arc<AtomicBool>,
        tap_installed: Arc<AtomicBool>,
    ) -> std::thread::JoinHandle<()> {
        std::thread::spawn(move || {
            let sink = Arc::clone(&events);
            let tap = CGEventTap::new(
                CGEventTapLocation::HID,
                CGEventTapPlacement::HeadInsertEventTap,
                // ListenOnly so we never delay or swallow the user's real input.
                CGEventTapOptions::ListenOnly,
                vec![
                    CGEventType::LeftMouseDown,
                    CGEventType::RightMouseDown,
                    CGEventType::OtherMouseDown,
                    CGEventType::LeftMouseUp,
                    CGEventType::RightMouseUp,
                    CGEventType::OtherMouseUp,
                    CGEventType::ScrollWheel,
                ],
                move |_proxy, etype, event| {
                    let kind = match etype {
                        CGEventType::LeftMouseDown
                        | CGEventType::RightMouseDown
                        | CGEventType::OtherMouseDown => EventKind::Down,
                        CGEventType::LeftMouseUp
                        | CGEventType::RightMouseUp
                        | CGEventType::OtherMouseUp => EventKind::Up,
                        CGEventType::ScrollWheel => EventKind::Scroll,
                        // A tap that trips the watchdog stops delivering entirely until
                        // re-armed, which would silently truncate rather than fail.
                        CGEventType::TapDisabledByTimeout | CGEventType::TapDisabledByUserInput => {
                            eprintln!("warning: event tap disabled by system; clicks truncated");
                            return CallbackResult::Keep;
                        }
                        _ => return CallbackResult::Keep,
                    };

                    let loc = event.location();
                    if let Ok(mut guard) = sink.lock() {
                        guard.push(TelemetryEvent {
                            t_ns: event_timestamp_ns(event),
                            kind,
                            x: loc.x,
                            y: loc.y,
                        });
                    }
                    CallbackResult::Keep
                },
            );

            let tap = match tap {
                Ok(t) => t,
                Err(()) => {
                    eprintln!(
                        "warning: could not install event tap — clicks will not be recorded. \
                         Grant Input Monitoring in System Settings > Privacy & Security."
                    );
                    return;
                }
            };

            let source = match tap.mach_port().create_runloop_source(0) {
                Ok(s) => s,
                Err(()) => {
                    eprintln!("warning: could not create runloop source; clicks unavailable");
                    return;
                }
            };
            CFRunLoop::get_current().add_source(&source, unsafe { kCFRunLoopCommonModes });
            tap.enable();
            tap_installed.store(true, Ordering::Relaxed);

            // Tick in slices rather than run_current() so the stop flag is seen promptly.
            while !stop.load(Ordering::Relaxed) {
                CFRunLoop::run_in_mode(
                    unsafe { kCFRunLoopDefaultMode },
                    std::time::Duration::from_millis(100),
                    false,
                );
            }
        })
    }

    pub fn stop(mut self) -> Telemetry {
        self.stop.store(true, Ordering::Relaxed);
        for h in [self.poll_handle.take(), self.tap_handle.take()]
            .into_iter()
            .flatten()
        {
            let _ = h.join();
        }
        let mut events = self.events.lock().unwrap().clone();
        events.sort_by_key(|e| e.t_ns);
        Telemetry {
            events,
            tap_installed: self.tap_installed.load(Ordering::Relaxed),
        }
    }
}
