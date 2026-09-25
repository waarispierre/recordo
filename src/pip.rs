//! Webcam picture-in-picture: corner + inset + size -> an output-pixel rectangle, and the
//! frame offset that lines the camera's own timeline up with the screen's.
//!
//! Pure geometry, no OS or GPU dependency, so it is unit-tested the way `camera.rs` is.

#[derive(Debug, Clone, Copy, PartialEq)]
pub struct Rect {
    pub x: f32,
    pub y: f32,
    pub w: f32,
    pub h: f32,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Corner {
    BottomLeft,
    BottomRight,
    TopLeft,
    TopRight,
}

impl Corner {
    pub fn parse(s: &str) -> Self {
        match s.to_ascii_lowercase().as_str() {
            "bottom-right" => Corner::BottomRight,
            "top-left" => Corner::TopLeft,
            "top-right" => Corner::TopRight,
            _ => Corner::BottomLeft,
        }
    }
}

/// Where the webcam sits and how big it is, in output pixels.
///
/// `size_percent` sets the height; `aspect` (camera width / height, 1.0 for a circle,
/// since that is cropped to a square before it reaches the renderer) derives the width
/// from it, so the box never distorts the camera's own picture. The position is clamped
/// to stay inside the frame — a large inset moves the box toward the opposite edge
/// rather than off it, the same way a window manager would.
pub fn pip_rect(
    out_w: f32,
    out_h: f32,
    corner: Corner,
    inset: f32,
    offset: (f32, f32),
    size_percent: f32,
    aspect: f32,
) -> Rect {
    let h = (out_h * (size_percent.max(0.0) / 100.0)).clamp(1.0, out_h);
    let w = (h * aspect.max(0.01)).clamp(1.0, out_w);
    let inset = inset.max(0.0);

    let (raw_x, raw_y) = match corner {
        Corner::BottomLeft => (inset, out_h - inset - h),
        Corner::BottomRight => (out_w - inset - w, out_h - inset - h),
        Corner::TopLeft => (inset, inset),
        Corner::TopRight => (out_w - inset - w, inset),
    };

    let x = (raw_x + offset.0).clamp(0.0, (out_w - w).max(0.0));
    let y = (raw_y + offset.1).clamp(0.0, (out_h - h).max(0.0));

    Rect { x, y, w, h }
}

/// How many output frames the camera's first sample sits away from the screen's.
///
/// Positive: the camera started this many frames *after* the screen — the overlay should
/// stay hidden for that many frames at the start. Negative: the camera started first, so
/// that many of its frames should be read and discarded before compositing begins. Both
/// streams are on the same host clock (`clock::now_nanos`, and the camera's own
/// `CMSampleBuffer` presentation time), so this is exact rather than an estimate.
pub fn frame_offset(screen_base_ns: u64, camera_first_ns: u64, fps: u64) -> i64 {
    let delta_ns = camera_first_ns as i128 - screen_base_ns as i128;
    let frame_ns = 1_000_000_000i128 / fps as i128;
    (delta_ns.div_euclid(frame_ns)) as i64
}

#[cfg(test)]
mod tests {
    use super::*;

    const OUT_W: f32 = 1920.0;
    const OUT_H: f32 = 1080.0;

    #[test]
    fn each_corner_lands_inside_the_frame() {
        for corner in [
            Corner::BottomLeft,
            Corner::BottomRight,
            Corner::TopLeft,
            Corner::TopRight,
        ] {
            let r = pip_rect(OUT_W, OUT_H, corner, 32.0, (0.0, 0.0), 18.0, 1.0);
            assert!(
                r.x >= 0.0 && r.y >= 0.0,
                "{corner:?} rect at negative origin"
            );
            assert!(
                r.x + r.w <= OUT_W && r.y + r.h <= OUT_H,
                "{corner:?} rect spills past the frame: {r:?}"
            );
        }
    }

    #[test]
    fn a_huge_inset_is_clamped_rather_than_pushed_off_frame() {
        let r = pip_rect(
            OUT_W,
            OUT_H,
            Corner::BottomLeft,
            5000.0,
            (0.0, 0.0),
            18.0,
            1.0,
        );
        assert!(r.x >= 0.0 && r.x + r.w <= OUT_W);
        assert!(r.y >= 0.0 && r.y + r.h <= OUT_H);
    }

    #[test]
    fn a_wide_offset_is_clamped_rather_than_pushed_off_frame() {
        let r = pip_rect(
            OUT_W,
            OUT_H,
            Corner::TopRight,
            32.0,
            (-9000.0, 9000.0),
            18.0,
            1.0,
        );
        assert!(r.x >= 0.0 && r.x + r.w <= OUT_W);
        assert!(r.y >= 0.0 && r.y + r.h <= OUT_H);
    }

    #[test]
    fn rect_width_follows_aspect_not_just_height() {
        let square = pip_rect(
            OUT_W,
            OUT_H,
            Corner::BottomLeft,
            32.0,
            (0.0, 0.0),
            18.0,
            1.0,
        );
        let wide = pip_rect(
            OUT_W,
            OUT_H,
            Corner::BottomLeft,
            32.0,
            (0.0, 0.0),
            18.0,
            16.0 / 9.0,
        );
        assert_eq!(square.h, wide.h);
        assert!(wide.w > square.w);
    }

    #[test]
    fn zero_offset_is_the_identity() {
        assert_eq!(frame_offset(1_000_000_000, 1_000_000_000, 60), 0);
    }

    #[test]
    fn positive_and_negative_offsets_are_symmetric() {
        let frame_ns = 1_000_000_000 / 60;
        let later = frame_offset(1_000_000_000, 1_000_000_000 + frame_ns * 5, 60);
        let earlier = frame_offset(1_000_000_000 + frame_ns * 5, 1_000_000_000, 60);
        assert_eq!(later, 5);
        assert_eq!(earlier, -5);
    }

    #[test]
    fn corner_parses_case_insensitively_and_defaults_to_bottom_left() {
        assert_eq!(Corner::parse("Bottom-Right"), Corner::BottomRight);
        assert_eq!(Corner::parse("top-left"), Corner::TopLeft);
        assert_eq!(Corner::parse("top-right"), Corner::TopRight);
        assert_eq!(Corner::parse("nonsense"), Corner::BottomLeft);
    }
}
