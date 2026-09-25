//! Screen capture with out-of-band cursor telemetry.

use crate::capture::{frames, telemetry, webarea};
use crate::config::Config;
use crate::session::Session;
use anyhow::{Context, Result, anyhow};
use screencapturekit::audio_devices::AudioInputDevice;
use screencapturekit::cg::{CGPoint, CGRect, CGSize};
use screencapturekit::prelude::*;
use screencapturekit::recording_output::{
    SCRecordingOutput, SCRecordingOutputCodec, SCRecordingOutputConfiguration,
    SCRecordingOutputFileType,
};
use std::sync::Arc;

/// What to point the camera at.
#[derive(Debug, Clone, Default)]
pub enum Target {
    /// Ask, listing what is on screen.
    #[default]
    Ask,
    Display,
    App(String),
    Window(u32),
}

pub struct Plan {
    pub target: Option<SCWindow>,
    pub label: String,
    pub capture_w: u32,
    pub capture_h: u32,
    pub scale: u32,
    /// Name of the microphone being recorded, or None when voice over is off. Resolved
    /// in `record`, since `plan` does not see the config.
    pub microphone: Option<String>,
}

/// Resolves what will be recorded, without starting anything.
pub fn plan(
    target: &Target,
    chooser: impl FnOnce(&[SCWindow]) -> Result<Option<SCWindow>>,
) -> Result<Plan> {
    let content = SCShareableContent::get().context(
        "could not read shareable content — grant Screen Recording permission to your terminal",
    )?;
    let display = content
        .displays()
        .into_iter()
        .next()
        .context("no displays found")?;

    let window =
        match target {
            Target::Display => None,
            Target::Ask => chooser(&crate::capture::windows::capturable(&content))?,
            Target::App(name) => {
                let mut hits: Vec<SCWindow> = crate::capture::windows::capturable(&content)
                    .into_iter()
                    .filter(|w| {
                        w.owning_application().is_some_and(|a| {
                            a.application_name()
                                .to_lowercase()
                                .contains(&name.to_lowercase())
                        })
                    })
                    .collect();
                // An app usually has several windows — panels, inspectors, popovers. The
                // largest is almost always the one worth recording.
                hits.sort_by(|a, b| {
                    let area = |w: &SCWindow| w.frame().size.width * w.frame().size.height;
                    area(b)
                        .partial_cmp(&area(a))
                        .unwrap_or(std::cmp::Ordering::Equal)
                });
                Some(hits.into_iter().next().ok_or_else(|| {
                    anyhow!("no window found for {name:?} — try `recordo windows`")
                })?)
            }
            Target::Window(id) => Some(
                crate::capture::windows::capturable(&content)
                    .into_iter()
                    .find(|w| w.window_id() == *id)
                    .ok_or_else(|| anyhow!("no window with id {id} — try `recordo windows`"))?,
            ),
        };

    let (dw, dh) = (display.width() as f64, display.height() as f64);
    let (rect, label) = match &window {
        Some(w) => {
            let f = w.frame();
            // SCWindow::frame is only trustworthy for windows on the current Space.
            // Windows parked elsewhere report an origin outside the display, which would
            // silently crop to an empty region and record pure black.
            if f.origin.x < 0.0
                || f.origin.y < 0.0
                || f.origin.x + f.size.width > dw + 1.0
                || f.origin.y + f.size.height > dh + 1.0
            {
                return Err(anyhow!(
                    "that window sits outside the {dw:.0}x{dh:.0} display, so it is on \
                     another Space. Switch to it first, or record the whole display."
                ));
            }
            (
                (f.origin.x, f.origin.y, f.size.width, f.size.height),
                crate::capture::windows::label(w),
            )
        }
        None => (
            (0.0, 0.0, dw, dh),
            format!("entire display ({dw:.0}x{dh:.0})"),
        ),
    };

    let scale = scale_for(&content, &window)?;
    Ok(Plan {
        target: window,
        label,
        capture_w: rect.2 as u32 * scale,
        capture_h: rect.3 as u32 * scale,
        scale,
        microphone: None,
    })
}

/// The microphone to record, as (device id, display name).
///
/// An empty `want` takes whatever macOS is currently set to, which is what most people
/// mean. A named one is matched the way `--app` matches a window: case-insensitive
/// substring, so "space q45" finds "soundcore Space Q45".
fn microphone(want: &str) -> Result<(Option<String>, String)> {
    let want = want.trim();
    if want.is_empty() {
        let name = AudioInputDevice::default_device()
            .map(|d| d.name)
            .unwrap_or_else(|| "system default".into());
        return Ok((None, name));
    }

    let devices = AudioInputDevice::list();
    let hit = devices
        .iter()
        .find(|d| d.name.to_lowercase().contains(&want.to_lowercase()));
    match hit {
        Some(d) => Ok((Some(d.id.clone()), d.name.clone())),
        None => {
            let known: Vec<&str> = devices.iter().map(|d| d.name.as_str()).collect();
            Err(anyhow!(
                "no microphone matching \"{want}\" — available: {}",
                if known.is_empty() {
                    "none".to_string()
                } else {
                    known.join(", ")
                }
            ))
        }
    }
}

fn scale_for(content: &SCShareableContent, window: &Option<SCWindow>) -> Result<u32> {
    let display = content
        .displays()
        .into_iter()
        .next()
        .context("no displays")?;
    let filter = match window {
        Some(w) => SCContentFilter::create()
            .with_display(&display)
            .with_including_windows(&[w])
            .try_build(),
        None => SCContentFilter::create().with_display(&display).try_build(),
    }
    .context("failed to build content filter")?;
    // Points, not pixels: capturing a Retina panel at 1x throws away half the detail,
    // which shows as soft text the moment the camera zooms in.
    Ok(filter.point_pixel_scale().round().max(1.0) as u32)
}

/// Records until `stop` returns, writing the capture and its sidecars into `session`.
pub fn record(
    session: &Session,
    target: &Target,
    config: &Config,
    chooser: impl FnOnce(&[SCWindow]) -> Result<Option<SCWindow>>,
    on_start: impl FnOnce(&Plan),
    stop: impl FnOnce(),
) -> Result<()> {
    let content = SCShareableContent::get().context("grant Screen Recording permission")?;
    let display = content
        .displays()
        .into_iter()
        .next()
        .context("no displays found")?;
    let mut plan = plan(target, chooser)?;

    // Resolved before the stream is built, so a bad device name fails immediately rather
    // than after a recording has already been made.
    let mic = if config.audio.microphone {
        let (id, name) = microphone(&config.audio.device)?;
        plan.microphone = Some(name);
        Some(id)
    } else {
        None
    };

    let filter = match &plan.target {
        // Deliberately not SCContentFilter(desktopIndependentWindow:) — that trips a
        // CGS_REQUIRE_INIT assertion in a plain CLI process. Filtering the display to one
        // window and cropping to its frame gets the same result.
        Some(w) => SCContentFilter::create()
            .with_display(&display)
            .with_including_windows(&[w])
            .try_build(),
        None => SCContentFilter::create().with_display(&display).try_build(),
    }
    .context("failed to build content filter")?;

    let rect = match &plan.target {
        Some(w) => {
            let f = w.frame();
            (f.origin.x, f.origin.y, f.size.width, f.size.height)
        }
        None => (0.0, 0.0, display.width() as f64, display.height() as f64),
    };

    let mut stream_config = SCStreamConfiguration::new()
        .with_width(plan.capture_w)
        .with_height(plan.capture_h)
        .with_fps(60)
        .with_shows_cursor(true);
    if let Some(device) = &mic {
        // Microphone only. `captures_audio` — system audio — stays off, so a notification
        // chime or whatever else is playing never lands in the recording.
        stream_config = stream_config.with_captures_microphone(true);
        if let Some(id) = device {
            stream_config = stream_config.with_microphone_capture_device_id(id);
        }
    }
    if plan.target.is_some() {
        stream_config = stream_config.with_source_rect(CGRect {
            origin: CGPoint {
                x: rect.0,
                y: rect.1,
            },
            size: CGSize {
                width: rect.2,
                height: rect.3,
            },
        });
    }

    let capture_path = session.capture();
    // SCRecordingOutput refuses to start if the destination already exists.
    let _ = std::fs::remove_file(&capture_path);

    let mut stream = SCStream::new(&filter, &stream_config);
    let frame_log = Arc::new(frames::FrameLog::default());
    stream.add_output_handler(
        frames::FrameLogHandler::new(Arc::clone(&frame_log)),
        SCStreamOutputType::Screen,
    );

    let rec_config = SCRecordingOutputConfiguration::new()
        .with_output_url(&capture_path)
        .with_video_codec(SCRecordingOutputCodec::H264)
        .with_output_file_type(SCRecordingOutputFileType::MP4);
    let recording =
        SCRecordingOutput::new(&rec_config).context("failed to create recording output")?;
    stream
        .add_recording_output(&recording)
        .context("failed to attach recording output")?;

    // Ask the browser where its page actually is, rather than guessing a fixed crop.
    // Read before recording starts so the layout matches the frames.
    let web_rect = plan.target.as_ref().and_then(|w| {
        let app = w.owning_application()?;
        if !crate::config::is_browser(&app.bundle_identifier()) {
            return None;
        }
        let f = w.frame();
        webarea::web_content_rect(
            app.process_id(),
            webarea::Rect {
                x: f.origin.x,
                y: f.origin.y,
                w: f.size.width,
                h: f.size.height,
            },
        )
    });

    let tel = telemetry::TelemetryRecorder::start();
    stream.start_capture().context("failed to start capture")?;
    on_start(&plan);
    stop();
    stream.stop_capture().context("failed to stop capture")?;

    let telemetry = tel.stop();
    let frame_times = frame_log.snapshot();

    // Sidecars describe where the cursor went for the whole session; keep them as
    // private as the video itself.
    crate::session::write_private(
        &session.telemetry(),
        &serde_json::to_vec_pretty(&telemetry)?,
    )?;
    crate::session::write_private(&session.frames(), &serde_json::to_vec_pretty(&frame_times)?)?;
    crate::session::write_private(
        &session.meta(),
        &serde_json::to_vec_pretty(&serde_json::json!({
            "display_w": display.width(),
            "display_h": display.height(),
            "capture_w": plan.capture_w,
            "capture_h": plan.capture_h,
            "scale": plan.scale,
            // Cursor telemetry is global and in points; the renderer subtracts this
            // origin and applies the scale to reach capture pixels.
            "origin_x": rect.0,
            "origin_y": rect.1,
            "source_w": rect.2,
            "source_h": rect.3,
            "bundle_id": plan.target.as_ref().and_then(|w| w.owning_application())
                .map(|a| a.bundle_identifier()),
            "app_name": plan.target.as_ref().and_then(|w| w.owning_application())
                .map(|a| a.application_name()),
            "web_rect": web_rect.map(|r| serde_json::json!({
                "x": r.x, "y": r.y, "w": r.w, "h": r.h
            })),
            "microphone": plan.microphone,
        }))?,
    )?;

    // ScreenCaptureKit writes the capture with the default umask.
    crate::session::restrict(&capture_path)?;

    Ok(())
}

/// Summary of how well cursor telemetry lines up with captured frames.
pub struct Health {
    pub frames: usize,
    pub cursor_samples: usize,
    pub clicks: usize,
    pub median_gap_ms: f64,
    pub tap_installed: bool,
}

pub fn health(session: &Session) -> Result<Health> {
    let tel: telemetry::Telemetry = serde_json::from_slice(&std::fs::read(session.telemetry())?)?;
    let frames: Vec<frames::FrameRecord> =
        serde_json::from_slice(&std::fs::read(session.frames())?)?;
    let timed: Vec<u64> = frames.iter().filter_map(|f| f.display_time_ns).collect();

    // Sampling is deduplicated while the cursor is parked, so one frame can legitimately
    // sit seconds from a sample. The median is what says the streams line up.
    let mut gaps: Vec<f64> = timed
        .iter()
        .step_by(10)
        .filter_map(|&t| {
            tel.events
                .iter()
                .map(|e| (e.t_ns as i128 - t as i128).unsigned_abs())
                .min()
                .map(|g| g as f64 / 1e6)
        })
        .collect();
    gaps.sort_by(f64::total_cmp);

    Ok(Health {
        frames: timed.len(),
        cursor_samples: tel.count(telemetry::EventKind::Move),
        clicks: tel.count(telemetry::EventKind::Down),
        median_gap_ms: gaps.get(gaps.len() / 2).copied().unwrap_or(f64::NAN),
        tap_installed: tel.tap_installed,
    })
}
