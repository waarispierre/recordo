//! The terminal interface.
//!
//! Binary-only: nothing in the library depends on these, so the capture, camera and
//! render layers stay usable without a terminal attached.
//!
//! - [`tui`] is the full-screen app shown when `recordo` is run bare.
//! - [`ui`] is the line-oriented output used by the subcommands, and by the full-screen
//!   app when it suspends itself for a recording or a render.

pub mod tui;
pub mod ui;
