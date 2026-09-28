//! Webcam capture via AVFoundation, run alongside the ScreenCaptureKit stream.
//!
//! Mirrors the screen side deliberately: `AVCaptureMovieFileOutput` writes `camera.mp4`
//! directly, the way `SCRecordingOutput` writes `capture.mp4`, so the pixels that end up
//! in the recording never pass through this process. A second output —
//! `AVCaptureVideoDataOutput` — exists purely to log each frame's presentation timestamp;
//! it never looks at the pixel data. That timestamp is on the host clock
//! (`CMClockGetHostTimeClock`), the same timebase `mach_absolute_time` reads, which is
//! what lets the render step line the webcam up against the screen instead of guessing an
//! offset from session start latency.
//!
//! A caller that asks for a [`PreviewSink`] gets a *third* output, which does read pixels
//! — heavily downsampled and throttled, and only to show a live thumbnail while recording.
//! It is deliberately separate from the timestamp tap, on its own queue, so the frame
//! timing the render step depends on is never delayed by a preview downsample.

use crate::capture::frames::FrameRecord;
use anyhow::{Context, Result, anyhow};
use dispatch2::{DispatchQueue, DispatchQueueAttr};
use objc2::rc::Retained;
use objc2::runtime::{AnyObject, ProtocolObject};
use objc2::{AnyThread, DefinedClass, define_class};
use objc2_av_foundation::{
    AVCaptureConnection, AVCaptureDevice, AVCaptureDeviceInput, AVCaptureFileOutput,
    AVCaptureFileOutputRecordingDelegate, AVCaptureMovieFileOutput, AVCaptureOutput,
    AVCaptureSession, AVCaptureSessionPresetHigh, AVCaptureVideoDataOutput,
    AVCaptureVideoDataOutputSampleBufferDelegate, AVMediaTypeVideo,
};
use objc2_core_media::CMSampleBuffer;
use objc2_core_video::{CVPixelBufferLockFlags, kCVPixelFormatType_32BGRA};
use objc2_foundation::{
    NSArray, NSDictionary, NSError, NSNumber, NSObject, NSObjectProtocol, NSString, NSURL,
    ns_string,
};
use std::path::Path;
use std::sync::atomic::{AtomicU32, Ordering};
use std::sync::{Arc, Mutex};

/// One camera frame's timestamp, host-clock nanoseconds — the same schema as
/// `frames::FrameRecord`, so the render side treats both streams identically.
type FrameLog = Arc<Mutex<Vec<FrameRecord>>>;

/// A small downsampled camera frame, for a caller that wants to show a live thumbnail
/// (the TUI's corner picture-in-picture) rather than the full-resolution capture.
#[derive(Clone)]
pub struct PreviewFrame {
    /// Row-major, top-to-bottom, 3 bytes (R, G, B) per pixel.
    pub rgb: Vec<u8>,
    pub w: u32,
    pub h: u32,
}

/// Where the newest [`PreviewFrame`] is published. Holds only the latest frame, never a
/// queue — a caller redrawing on its own cadence (the TUI's draw loop) only ever wants
/// "what does the camera see right now", not a backlog to catch up on.
pub type PreviewSink = Arc<Mutex<Option<PreviewFrame>>>;

/// Longest edge of a [`PreviewFrame`], in pixels. Small on purpose: this is downsampled
/// again to a handful of terminal cells, so there is nothing to gain from copying more.
const PREVIEW_SIZE: u32 = 48;

/// Only every Nth sample buffer is turned into a [`PreviewFrame`] — at a 30fps session
/// this is close to 5fps, which is plenty for a framing check and keeps the extra tap
/// cheap next to the timestamp-only one that already runs on every frame.
const PREVIEW_EVERY_N: u32 = 6;

define_class!(
    #[unsafe(super(NSObject))]
    #[name = "RecordoSampleTimestampDelegate"]
    #[ivars = FrameLog]
    struct TimestampDelegate;

    unsafe impl NSObjectProtocol for TimestampDelegate {}

    unsafe impl AVCaptureVideoDataOutputSampleBufferDelegate for TimestampDelegate {
        #[unsafe(method(captureOutput:didOutputSampleBuffer:fromConnection:))]
        fn did_output_sample_buffer(
            &self,
            _output: &AVCaptureOutput,
            sample_buffer: &CMSampleBuffer,
            _connection: &AVCaptureConnection,
        ) {
            // Safe: reading the buffer's own presentation timestamp, no ownership taken.
            let pts = unsafe { sample_buffer.presentation_time_stamp() };
            // Safe: CMTime::seconds reads a plain numeric struct.
            let seconds = unsafe { pts.seconds() };
            if !seconds.is_finite() {
                return;
            }
            let record = FrameRecord {
                pts_ns: Some((seconds * 1e9) as u64),
                display_time_ns: Some((seconds * 1e9) as u64),
            };
            if let Ok(mut guard) = self.ivars().lock() {
                guard.push(record);
            }
        }
    }
);

impl TimestampDelegate {
    fn new(log: FrameLog) -> Retained<Self> {
        let this = Self::alloc().set_ivars(log);
        // Safe: NSObject's init has no preconditions.
        unsafe { objc2::msg_send![super(this), init] }
    }
}

define_class!(
    #[unsafe(super(NSObject))]
    #[name = "RecordoRecordingDelegate"]
    #[ivars = ()]
    struct RecordingDelegate;

    unsafe impl NSObjectProtocol for RecordingDelegate {}

    unsafe impl AVCaptureFileOutputRecordingDelegate for RecordingDelegate {
        #[unsafe(method(captureOutput:didFinishRecordingToOutputFileAtURL:fromConnections:error:))]
        fn did_finish(
            &self,
            _output: &AVCaptureFileOutput,
            _file_url: &NSURL,
            _connections: &NSArray<AVCaptureConnection>,
            error: Option<&NSError>,
        ) {
            // Nothing to do: `stop()` blocks on `isRecording` rather than waiting on this
            // callback, since the delegate can fire on a queue we do not control and we
            // have no async runtime here to hand it to. A non-nil error only means the
            // encoded file may be short or missing a final sample; the caller ends up
            // with whatever `camera.mp4` contains, same as any other interrupted capture.
            let _ = error;
        }
    }
);

impl RecordingDelegate {
    fn new() -> Retained<Self> {
        let this = Self::alloc().set_ivars(());
        // Safe: NSObject's init has no preconditions.
        unsafe { objc2::msg_send![super(this), init] }
    }
}

/// Sink to publish into, plus a counter for throttling — every `PREVIEW_EVERY_N`th
/// callback does the work, the rest return immediately.
type PreviewIvars = (PreviewSink, Arc<AtomicU32>);

define_class!(
    #[unsafe(super(NSObject))]
    #[name = "RecordoPreviewDelegate"]
    #[ivars = PreviewIvars]
    struct PreviewDelegate;

    unsafe impl NSObjectProtocol for PreviewDelegate {}

    unsafe impl AVCaptureVideoDataOutputSampleBufferDelegate for PreviewDelegate {
        #[unsafe(method(captureOutput:didOutputSampleBuffer:fromConnection:))]
        fn did_output_sample_buffer(
            &self,
            _output: &AVCaptureOutput,
            sample_buffer: &CMSampleBuffer,
            _connection: &AVCaptureConnection,
        ) {
            let (sink, counter) = self.ivars();
            if counter.fetch_add(1, Ordering::Relaxed) % PREVIEW_EVERY_N != 0 {
                return;
            }
            // Safe: reading the sample buffer's own image buffer; the returned value is
            // already retained by `image_buffer()` itself.
            let Some(image) = (unsafe { sample_buffer.image_buffer() }) else {
                return;
            };
            let Some(frame) = downsample(&image) else {
                return;
            };
            if let Ok(mut guard) = sink.lock() {
                *guard = Some(frame);
            }
        }
    }
);

impl PreviewDelegate {
    fn new(sink: PreviewSink) -> Retained<Self> {
        let this = Self::alloc().set_ivars((sink, Arc::new(AtomicU32::new(0))));
        // Safe: NSObject's init has no preconditions.
        unsafe { objc2::msg_send![super(this), init] }
    }
}

/// Video settings that force BGRA, so a pixel is always 4 interleaved bytes in a known
/// order. Without this, AVFoundation is free to deliver the camera's native format
/// (commonly biplanar YCbCr), which [`downsample`] does not understand.
fn bgra_settings() -> Retained<NSDictionary<NSString, AnyObject>> {
    let value = NSNumber::numberWithUnsignedInt(kCVPixelFormatType_32BGRA);
    NSDictionary::from_slices(&[ns_string!("PixelFormatType")], &[value.as_ref()])
}

/// Locks `image`, copies a nearest-neighbour downsampled RGB thumbnail no larger than
/// [`PREVIEW_SIZE`] on its longest edge, and unlocks it again. `None` on any failure —
/// a dropped preview frame is never worth failing the recording over.
fn downsample(image: &objc2_core_video::CVImageBuffer) -> Option<PreviewFrame> {
    // Safe: CVPixelBuffer and CVImageBuffer are the same type; locking read-only before
    // touching the base address is the documented way to access pixels from the CPU, and
    // is undone by the matching unlock below before this function returns.
    let locked = unsafe {
        objc2_core_video::CVPixelBufferLockBaseAddress(image, CVPixelBufferLockFlags::ReadOnly)
    };
    if locked != 0 {
        return None;
    }

    let src_w = objc2_core_video::CVPixelBufferGetWidth(image);
    let src_h = objc2_core_video::CVPixelBufferGetHeight(image);
    let stride = objc2_core_video::CVPixelBufferGetBytesPerRow(image);
    let base = objc2_core_video::CVPixelBufferGetBaseAddress(image);

    let frame = (!base.is_null() && src_w > 0 && src_h > 0).then(|| {
        // Safe: the buffer is locked for the duration of this closure and `stride * src_h`
        // is exactly the byte range CVPixelBuffer documents `GetBaseAddress` as owning for
        // a chunky (non-planar) format, which BGRA — forced via `bgra_settings` — is.
        let bytes = unsafe { std::slice::from_raw_parts(base as *const u8, stride * src_h) };
        let (out_w, out_h) = scaled(src_w as u32, src_h as u32, PREVIEW_SIZE);
        let mut rgb = vec![0u8; (out_w * out_h * 3) as usize];
        for y in 0..out_h {
            let sy = (y * src_h as u32 / out_h) as usize;
            for x in 0..out_w {
                let sx = (x * src_w as u32 / out_w) as usize;
                let i = sy * stride + sx * 4;
                if i + 3 >= bytes.len() {
                    continue;
                }
                let o = ((y * out_w + x) * 3) as usize;
                // BGRA in memory -> RGB out.
                rgb[o] = bytes[i + 2];
                rgb[o + 1] = bytes[i + 1];
                rgb[o + 2] = bytes[i];
            }
        }
        PreviewFrame {
            rgb,
            w: out_w,
            h: out_h,
        }
    });

    // Safe: symmetric unlock for the lock above, same flags as required by the API.
    unsafe {
        objc2_core_video::CVPixelBufferUnlockBaseAddress(image, CVPixelBufferLockFlags::ReadOnly)
    };
    frame
}

/// Scales `(w, h)` down so the longer edge is `max_edge`, preserving aspect ratio.
fn scaled(w: u32, h: u32, max_edge: u32) -> (u32, u32) {
    if w >= h {
        let out_w = max_edge.max(1);
        (out_w, (h * out_w / w.max(1)).max(1))
    } else {
        let out_h = max_edge.max(1);
        ((w * out_h / h.max(1)).max(1), out_h)
    }
}

/// A running webcam capture. Dropping this without calling `stop` leaves `camera.mp4`
/// unfinalized, so callers must call `stop` explicitly — matching `TelemetryRecorder`.
pub struct Webcam {
    session: Retained<AVCaptureSession>,
    movie_output: Retained<AVCaptureMovieFileOutput>,
    recording_delegate: Retained<RecordingDelegate>,
    // Kept alive for the session's lifetime: AVCaptureVideoDataOutput holds its delegate
    // unretained, so dropping this early would leave a dangling delegate pointer.
    _timestamp_delegate: Retained<TimestampDelegate>,
    _preview_delegate: Option<Retained<PreviewDelegate>>,
    frame_log: FrameLog,
}

/// Resolves a device by name, the same case-insensitive substring match `--app` and the
/// microphone setting use, or the system default when `want` is empty.
fn find_device(want: &str) -> Result<Retained<AVCaptureDevice>> {
    let want = want.trim();
    // Safe: reading a framework string constant.
    let media = unsafe { AVMediaTypeVideo }.context("AVMediaTypeVideo unavailable")?;
    if want.is_empty() {
        // Safe: a pure query for the currently preferred camera.
        return unsafe { AVCaptureDevice::defaultDeviceWithMediaType(media) }
            .context("no camera available");
    }
    for d in crate::capture::devices::cameras() {
        if d.name.to_lowercase().contains(&want.to_lowercase()) {
            let id = NSString::from_str(&d.id);
            // Safe: a pure lookup by an ID this process just enumerated.
            if let Some(dev) = unsafe { AVCaptureDevice::deviceWithUniqueID(&id) } {
                return Ok(dev);
            }
        }
    }
    let known: Vec<String> = crate::capture::devices::cameras()
        .into_iter()
        .map(|d| d.name)
        .collect();
    Err(anyhow!(
        "no camera matching \"{want}\" — available: {}",
        if known.is_empty() {
            "none".to_string()
        } else {
            known.join(", ")
        }
    ))
}

impl Webcam {
    /// Starts recording `device` (by name; empty for the system default) to `path`.
    ///
    /// A failure here is meant to be recoverable by the caller: a camera that is missing,
    /// in use, or denied should degrade a recording to screen-only, not fail it, the way
    /// a failed click-event tap degrades to `tap_installed: false`.
    ///
    /// `preview`, when given, gets a downsampled thumbnail every so often for the
    /// lifetime of the recording — see [`PreviewSink`].
    pub fn start(
        device: &str,
        path: &Path,
        preview: Option<PreviewSink>,
    ) -> Result<(Self, String)> {
        let cam = find_device(device)?;
        // Safe: localizedName is a plain string accessor.
        let name = unsafe { cam.localizedName() }.to_string();

        // Safe: opens the device for capture; released when `input`/`session` drop.
        let input = unsafe { AVCaptureDeviceInput::deviceInputWithDevice_error(&cam) }
            .map_err(|e| anyhow!("could not open camera: {e}"))?;

        // Safe: AVCaptureSession has no preconditions on construction.
        let session = unsafe { AVCaptureSession::new() };
        // Safe: presets are static framework constants; High matches the camera's own
        // capabilities rather than forcing a resolution the render side has to rescale
        // from, which ffmpeg's `-vf scale` in the render step already handles.
        // Safe: setSessionPreset is documented to accept this constant before the session
        // starts running.
        unsafe { session.setSessionPreset(AVCaptureSessionPresetHigh) };

        // Safe: canAddInput/addInput are pure session-graph mutation, called before the
        // session starts running.
        if !unsafe { session.canAddInput(&input) } {
            anyhow::bail!("camera input rejected by capture session");
        }
        unsafe { session.addInput(&input) };

        let movie_output = unsafe { AVCaptureMovieFileOutput::new() };
        if !unsafe { session.canAddOutput(&movie_output) } {
            anyhow::bail!("movie file output rejected by capture session");
        }
        unsafe { session.addOutput(&movie_output) };

        let video_data_output = unsafe { AVCaptureVideoDataOutput::new() };
        let frame_log: FrameLog = Arc::new(Mutex::new(Vec::new()));
        let timestamp_delegate = TimestampDelegate::new(Arc::clone(&frame_log));
        let queue = DispatchQueue::new("recordo.webcam.timestamps", DispatchQueueAttr::SERIAL);
        // Safe: the delegate and queue both outlive the session (held in `Webcam`), and
        // the protocol object is a thin wrapper over the same retained pointer.
        unsafe {
            video_data_output.setSampleBufferDelegate_queue(
                Some(ProtocolObject::from_ref(&*timestamp_delegate)),
                Some(&queue),
            )
        };
        if unsafe { session.canAddOutput(&video_data_output) } {
            unsafe { session.addOutput(&video_data_output) };
        }
        // A missing timestamp stream degrades sync, not the recording itself: the render
        // step falls back to treating the camera as starting at the same instant as the
        // screen, which is wrong only by the camera's own startup latency.

        // A second, throttled tap for a live thumbnail — its own output and queue, so a
        // slow downsample never delays the timestamp stream the render step depends on.
        let preview_delegate = if let Some(sink) = preview {
            let preview_output = unsafe { AVCaptureVideoDataOutput::new() };
            // Safe: setVideoSettings copies the dictionary; forcing BGRA is required here
            // — AVFoundation does not default to it, and `downsample` assumes 4
            // interleaved bytes per pixel in that order.
            unsafe { preview_output.setVideoSettings(Some(&bgra_settings())) };
            let delegate = PreviewDelegate::new(sink);
            let queue = DispatchQueue::new("recordo.webcam.preview", DispatchQueueAttr::SERIAL);
            // Safe: same contract as the timestamp output above — delegate and queue both
            // outlive the session, held in `Webcam`.
            unsafe {
                preview_output.setSampleBufferDelegate_queue(
                    Some(ProtocolObject::from_ref(&*delegate)),
                    Some(&queue),
                )
            };
            if unsafe { session.canAddOutput(&preview_output) } {
                unsafe { session.addOutput(&preview_output) };
                Some(delegate)
            } else {
                None
            }
        } else {
            None
        };

        // Safe: starts hardware capture; this is the documented way to begin.
        unsafe { session.startRunning() };

        let _ = std::fs::remove_file(path);
        let url_string = NSString::from_str(&path.to_string_lossy());
        // fileURLWithPath is a plain, safe constructor.
        let url = NSURL::fileURLWithPath(&url_string);
        let recording_delegate = RecordingDelegate::new();
        // Safe: starts writing to `url`; the delegate outlives the recording (held in
        // `Webcam`).
        unsafe {
            movie_output.startRecordingToOutputFileURL_recordingDelegate(
                &url,
                ProtocolObject::from_ref(&*recording_delegate),
            )
        };

        Ok((
            Self {
                session,
                movie_output,
                recording_delegate,
                _timestamp_delegate: timestamp_delegate,
                _preview_delegate: preview_delegate,
                frame_log,
            },
            name,
        ))
    }

    /// Stops recording and returns the frame timestamps captured while it ran.
    ///
    /// Blocks briefly on `isRecording`, since `AVCaptureFileOutput` finishes writing the
    /// trailing samples asynchronously after `stopRecording` returns.
    pub fn stop(self) -> Vec<FrameRecord> {
        // Safe: a documented way to end a recording in progress.
        unsafe { self.movie_output.stopRecording() };
        let deadline = std::time::Instant::now() + std::time::Duration::from_secs(3);
        // Safe: isRecording is a plain property read.
        while unsafe { self.movie_output.isRecording() } && std::time::Instant::now() < deadline {
            std::thread::sleep(std::time::Duration::from_millis(20));
        }
        // Safe: stopRunning is the documented way to tear down the session.
        unsafe { self.session.stopRunning() };
        let _ = &self.recording_delegate;
        self.frame_log.lock().map(|g| g.clone()).unwrap_or_default()
    }
}
