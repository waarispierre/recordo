//! Renders a recording with cursor-following auto-zoom.
//!
//! Decodes the raw capture through ffmpeg, composites each frame on the GPU using the
//! camera model, and pipes the result to a hardware H.264 encoder.

use crate::camera::{self, Crop};
use crate::capture::clock;
use crate::capture::frames::FrameRecord;
use crate::capture::telemetry::Telemetry;
use crate::config::Config;
use crate::render::Chrome;
use crate::render::{self as render, BackgroundImage, Renderer};
use anyhow::{Context, Result, anyhow};
use std::io::{Read, Write};
use std::process::{Command, Stdio};

const FPS: u64 = 60;

/// Composites `src` into `dst` using the sidecars written alongside the recording.
/// What a render did, for the caller to present.
#[derive(Debug, Clone)]
pub struct Report {
    pub frames: usize,
    pub video_seconds: f64,
    pub render_seconds: f64,
    pub output: (u32, u32),
    pub chrome: Chrome,
    /// How the browser page region was determined, if at all.
    pub crop_source: CropSource,
    pub app_name: String,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum CropSource {
    /// Whole capture used.
    None,
    /// Exact page bounds from the accessibility tree.
    Accessibility,
    /// Fixed `browser_crop_top` guess, because the real bounds were unavailable.
    FixedGuess,
}

impl Report {
    pub fn realtime_ratio(&self) -> f64 {
        if self.render_seconds > 0.0 {
            self.video_seconds / self.render_seconds
        } else {
            0.0
        }
    }
    pub fn fps(&self) -> f64 {
        if self.render_seconds > 0.0 {
            self.frames as f64 / self.render_seconds
        } else {
            0.0
        }
    }
}

pub fn run(src: &str, dst: &str) -> Result<()> {
    run_with(src, dst, None).map(|_| ())
}

/// As [`run`], with an optional zoom-percentage override for this render only.
pub fn run_with(src: &str, dst: &str, zoom_percent: Option<f64>) -> Result<Report> {
    // ffmpeg has no `--` terminator, so a path starting with '-' becomes an option.
    crate::tools::check_not_option_like("input", src)?;
    crate::tools::check_not_option_like("output", dst)?;

    // Sidecars live beside the capture, so a recording stays self-contained wherever it
    // is and the tool works from any working directory.
    let dir = std::path::Path::new(src)
        .parent()
        .unwrap_or_else(|| std::path::Path::new("."));
    let sidecar = |name: &str| dir.join(name);

    let mut tel: Telemetry = serde_json::from_slice(&std::fs::read(sidecar("telemetry.json"))?)
        .with_context(|| format!("read {}", sidecar("telemetry.json").display()))?;
    let frames: Vec<FrameRecord> = serde_json::from_slice(&std::fs::read(sidecar("frames.json"))?)
        .with_context(|| format!("read {}", sidecar("frames.json").display()))?;

    let (w, h, duration_s) = probe_video(src)?;

    // Decode at a fixed cadence so frame i is exactly i/FPS into the recording. The
    // capture's own frame timing is variable, so this resampling is what lets the
    // camera solve line up with what ffmpeg actually emits.
    let base_t = frames
        .iter()
        .filter_map(|f| f.display_time_ns)
        .next()
        .ok_or_else(|| anyhow!("no frame carried a display_time"))?;

    // Solve for exactly as many frames as the fps filter will emit, so the camera does
    // not freeze on the tail of the clip.
    let n_frames = (duration_s * FPS as f64).round() as u64 + 1;
    let times: Vec<u64> = (0..n_frames)
        .map(|i| base_t + i * 1_000_000_000 / FPS)
        .collect();

    // Telemetry is recorded in logical points; scale it into the captured pixel grid.
    let meta: serde_json::Value = serde_json::from_slice(
        &std::fs::read(sidecar("meta.json")).unwrap_or_else(|_| b"{}".to_vec()),
    )
    .unwrap_or(serde_json::Value::Null);
    let source_w = meta
        .get("source_w")
        .or_else(|| meta.get("display_w"))
        .and_then(|v| v.as_f64())
        .unwrap_or(w as f64);
    let origin_x = meta.get("origin_x").and_then(|v| v.as_f64()).unwrap_or(0.0);
    let origin_y = meta.get("origin_y").and_then(|v| v.as_f64()).unwrap_or(0.0);
    let point_scale = w as f64 / source_w;

    // Cursor telemetry is global and in points; the capture may be a window somewhere on
    // screen, recorded at Retina scale. Translate, then scale.
    if origin_x != 0.0 || origin_y != 0.0 || (point_scale - 1.0).abs() > 1e-6 {
        for e in &mut tel.events {
            e.x = (e.x - origin_x) * point_scale;
            e.y = (e.y - origin_y) * point_scale;
        }
    }

    let mut config = Config::load_or_create(crate::session::config_path()?)?;
    if let Some(z) = zoom_percent {
        config.camera.zoom_percent = z;
    }
    let bundle_id = meta.get("bundle_id").and_then(|v| v.as_str());
    let app_name = meta
        .get("app_name")
        .and_then(|v| v.as_str())
        .unwrap_or("display");
    let (chrome_mode, crop_top_pt) = config.resolve_chrome(bundle_id);
    let style = config.style(bundle_id);

    // For browsers, exclude the real tab/address strip so the drawn frame replaces it —
    // that is what removes tabs, bookmarks, the profile avatar and URL history.
    //
    // Prefer the page bounds the browser itself reported at capture time; a fixed crop is
    // only a fallback, since it breaks when a bookmarks bar is toggled and differs per
    // browser.
    let mut crop_source = CropSource::None;
    let web = meta.get("web_rect").filter(|v| !v.is_null());
    let content = match (web, chrome_mode) {
        (Some(r), Chrome::Browser) => {
            let g = |k: &str| r.get(k).and_then(|v| v.as_f64()).unwrap_or(0.0);
            // Web rect is in screen points; convert to capture pixels.
            let rect = ContentRect {
                x: ((g("x") - origin_x) * point_scale).max(0.0),
                y: ((g("y") - origin_y) * point_scale).max(0.0),
                w: g("w") * point_scale,
                h: g("h") * point_scale,
            };
            crop_source = CropSource::Accessibility;
            rect
        }
        (_, Chrome::Browser) => {
            let inset = (crop_top_pt as f64 * point_scale)
                .min(h as f64 - 1.0)
                .max(0.0);
            crop_source = CropSource::FixedGuess;
            ContentRect {
                x: 0.0,
                y: inset,
                w: w as f64,
                h: h as f64 - inset,
            }
        }
        _ => ContentRect {
            x: 0.0,
            y: 0.0,
            w: w as f64,
            h: h as f64,
        },
    };
    // Never let a stale rect run past the captured frame.
    let content = content.clamped(w as f64, h as f64);

    // Solve in content space, then shift the crops back into full-frame coordinates.
    for e in &mut tel.events {
        e.x -= content.x;
        e.y -= content.y;
    }
    let cfg = config.camera();
    let crops: Vec<Crop> = camera::solve(&tel, &times, content.w, content.h, &cfg)
        .into_iter()
        .map(|c| Crop {
            x: c.x + content.x,
            y: c.y + content.y,
            ..c
        })
        .collect();

    let background = match config.background_path()? {
        Some(path) => Some(BackgroundImage::load(&path)?),
        None => None,
    };

    // Style is authored in points; scale it to the captured pixel grid.
    let style = style.scaled(point_scale as f32);

    let (content_w_px, content_h_px) = (content.w.round() as u32, content.h.round() as u32);
    let (out_w, out_h) = render::output_size(content_w_px, content_h_px, &style);
    let renderer = Renderer::new(w, h, out_w, out_h, style, background)?;

    let ffmpeg = crate::tools::require("ffmpeg")?;
    let mut decoder = Command::new(&ffmpeg)
        .args([
            "-v",
            "error",
            // Never follow a reference out of the input file: ffmpeg's hls/concat/dash
            // demuxers will otherwise fetch remote URLs named inside a file, which is the
            // one way data here could reach the network. `fd` and `pipe` are the local
            // plumbing between these two processes, not network protocols.
            "-nostdin",
            "-protocol_whitelist",
            "file,pipe,fd",
            "-i",
            src,
            "-vf",
            &format!("fps={FPS}"),
            "-f",
            "rawvideo",
            "-pix_fmt",
            "rgba",
            "-",
        ])
        .stdout(Stdio::piped())
        .spawn()
        .context("spawn ffmpeg decoder")?;

    let mut encoder = Command::new(&ffmpeg)
        .args([
            "-v",
            "error",
            "-protocol_whitelist",
            "file,pipe,fd",
            "-y",
            "-f",
            "rawvideo",
            "-pix_fmt",
            "rgba",
            "-s",
            &format!("{out_w}x{out_h}"),
            "-r",
            &FPS.to_string(),
            "-i",
            "-",
            "-c:v",
            "h264_videotoolbox",
            "-b:v",
            "12M",
            "-pix_fmt",
            "yuv420p",
            dst,
        ])
        .stdin(Stdio::piped())
        .spawn()
        .context("spawn ffmpeg encoder")?;

    let mut dec_out = decoder.stdout.take().unwrap();
    let mut enc_in = encoder.stdin.take().unwrap();

    let frame_bytes = (w * h * 4) as usize;
    let mut src_buf = vec![0u8; frame_bytes];
    let mut out_buf: Vec<u8> = Vec::with_capacity(renderer.out_frame_bytes());

    let start = clock::now_nanos();
    let mut rendered = 0usize;

    loop {
        match dec_out.read_exact(&mut src_buf) {
            Ok(()) => {}
            // A short final read just means the stream ended.
            Err(e) if e.kind() == std::io::ErrorKind::UnexpectedEof => break,
            Err(e) => return Err(e).context("read decoded frame"),
        }

        // Hold the last solved crop if ffmpeg emits more frames than we solved for.
        let crop = *crops
            .get(rendered)
            .or_else(|| crops.last())
            .ok_or_else(|| anyhow!("camera produced no crops"))?;

        renderer.render(&src_buf, crop, &mut out_buf)?;
        enc_in
            .write_all(&out_buf)
            .context("write frame to encoder")?;
        rendered += 1;
    }

    drop(enc_in);
    let elapsed_ns = clock::now_nanos() - start;
    decoder.wait().ok();
    let status = encoder.wait().context("encoder wait")?;
    if !status.success() {
        return Err(anyhow!("ffmpeg encoder exited with {status}"));
    }

    let elapsed_s = elapsed_ns as f64 / 1e9;
    let video_s = rendered as f64 / FPS as f64;
    // ffmpeg creates the output with the default umask.
    crate::session::restrict(std::path::Path::new(dst))?;

    let report = Report {
        frames: rendered,
        video_seconds: video_s,
        render_seconds: elapsed_s,
        output: (out_w, out_h),
        chrome: chrome_mode,
        crop_source,
        app_name: app_name.to_string(),
    };

    Ok(report)
}

/// Region of the captured frame that holds the content worth showing, in capture pixels.
#[derive(Debug, Clone, Copy)]
struct ContentRect {
    x: f64,
    y: f64,
    w: f64,
    h: f64,
}

impl ContentRect {
    fn clamped(self, max_w: f64, max_h: f64) -> Self {
        let x = self.x.clamp(0.0, (max_w - 1.0).max(0.0));
        let y = self.y.clamp(0.0, (max_h - 1.0).max(0.0));
        Self {
            x,
            y,
            w: self.w.min(max_w - x).max(1.0),
            h: self.h.min(max_h - y).max(1.0),
        }
    }
}

fn probe_video(path: &str) -> Result<(u32, u32, f64)> {
    let out = Command::new(crate::tools::require("ffprobe")?)
        .args([
            "-v",
            "error",
            "-protocol_whitelist",
            "file,pipe,fd",
            "-select_streams",
            "v:0",
            "-show_entries",
            "stream=width,height",
            "-show_entries",
            "format=duration",
            "-of",
            "default=noprint_wrappers=1:nokey=1",
            path,
        ])
        .output()
        .context("run ffprobe")?;
    let text = String::from_utf8_lossy(&out.stdout);
    let vals: Vec<&str> = text.split_whitespace().collect();
    if vals.len() < 3 {
        return Err(anyhow!("unexpected ffprobe output: {text:?}"));
    }
    Ok((vals[0].parse()?, vals[1].parse()?, vals[2].parse()?))
}
