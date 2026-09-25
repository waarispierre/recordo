//! recordo — screen recordings with a cursor-following camera.
//!
//! The work splits into three layers that can be built and tested independently, which
//! is why they are separate modules rather than one flat namespace:
//!
//! - [`capture`] talks to the operating system: screen frames, cursor telemetry, window
//!   selection. Everything platform-specific lives here.
//! - [`camera`] turns that telemetry into a crop rectangle per frame. Pure maths, no OS
//!   or GPU dependency, so it is the layer that can be properly unit-tested — and the
//!   one where the product's perceived quality actually lives.
//! - [`render`] composites frames on the GPU and encodes the result.
//!
//! The terminal interface sits outside the library, in the binary's own `app` module.

pub mod camera;
pub mod capture;
pub mod config;
pub mod render;
pub mod session;
pub mod tools;
