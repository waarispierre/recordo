//! Recording the screen and what the cursor was doing.
//!
//! This is the only platform-specific layer. Everything here is macOS-only today:
//! ScreenCaptureKit for frames, a CGEventTap for clicks, and the Accessibility API for a
//! browser's page bounds. A Windows port would add siblings to these modules rather than
//! changing anything above.

pub mod clock;
pub mod devices;
pub mod frames;
pub mod recorder;
pub mod telemetry;
pub mod windows;

#[cfg(target_os = "macos")]
pub mod webarea;
