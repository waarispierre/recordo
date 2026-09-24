//! Phase 0 spike internals, shared by the recorder and exporter binaries.

pub mod camera;
pub mod clock;
pub mod config;
pub mod exporter;
pub mod frames;
pub mod pick;
pub mod recorder;
pub mod render;
pub mod session;
pub mod telemetry;
pub mod tools;

#[cfg(target_os = "macos")]
pub mod webarea;
