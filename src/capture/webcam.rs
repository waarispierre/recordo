//! Webcam capture via AVFoundation, run alongside the ScreenCaptureKit stream.
//!
//! Mirrors the screen side deliberately: `AVCaptureMovieFileOutput` writes `camera.mp4`
//! directly, the way `SCRecordingOutput` writes `capture.mp4`, so pixels never pass
//! through this process during recording. A second output — `AVCaptureVideoDataOutput` —
//! exists purely to log each frame's presentation timestamp; it never looks at the pixel
//! data. That timestamp is on the host clock (`CMClockGetHostTimeClock`), the same
//! timebase `mach_absolute_time` reads, which is what lets the render step line the
//! webcam up against the screen instead of guessing an offset from session start latency.

use crate::capture::frames::FrameRecord;
use anyhow::{Context, Result, anyhow};
use dispatch2::{DispatchQueue, DispatchQueueAttr};
use objc2::rc::Retained;
use objc2::runtime::ProtocolObject;
use objc2::{AnyThread, DefinedClass, define_class};
use objc2_av_foundation::{
    AVCaptureConnection, AVCaptureDevice, AVCaptureDeviceInput, AVCaptureFileOutput,
    AVCaptureFileOutputRecordingDelegate, AVCaptureMovieFileOutput, AVCaptureOutput,
    AVCaptureSession, AVCaptureSessionPresetHigh, AVCaptureVideoDataOutput,
    AVCaptureVideoDataOutputSampleBufferDelegate, AVMediaTypeVideo,
};
use objc2_core_media::CMSampleBuffer;
use objc2_foundation::{NSArray, NSError, NSObject, NSObjectProtocol, NSString, NSURL};
use std::path::Path;
use std::sync::{Arc, Mutex};

/// One camera frame's timestamp, host-clock nanoseconds — the same schema as
/// `frames::FrameRecord`, so the render side treats both streams identically.
type FrameLog = Arc<Mutex<Vec<FrameRecord>>>;

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

/// A running webcam capture. Dropping this without calling `stop` leaves `camera.mp4`
/// unfinalized, so callers must call `stop` explicitly — matching `TelemetryRecorder`.
pub struct Webcam {
    session: Retained<AVCaptureSession>,
    movie_output: Retained<AVCaptureMovieFileOutput>,
    recording_delegate: Retained<RecordingDelegate>,
    // Kept alive for the session's lifetime: AVCaptureVideoDataOutput holds its delegate
    // unretained, so dropping this early would leave a dangling delegate pointer.
    _timestamp_delegate: Retained<TimestampDelegate>,
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
    pub fn start(device: &str, path: &Path) -> Result<(Self, String)> {
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
