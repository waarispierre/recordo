//! Cameras and microphones: what macOS will let us use.
//!
//! Permission is read with `AVCaptureDevice.authorizationStatus`, which reports the
//! current grant **without** prompting. That matters for `doctor`: a diagnostic command
//! that pops a system dialog is a diagnostic command nobody runs twice.
//!
//! Like the other permissions this tool needs, camera and microphone access is granted to
//! the terminal recordo runs from rather than to the binary, because a bare Mach-O with
//! no bundle has no identity of its own for TCC to attach a grant to.

use objc2_av_foundation::{
    AVAuthorizationStatus, AVCaptureDevice, AVCaptureDeviceDiscoverySession,
    AVCaptureDevicePosition, AVCaptureDeviceTypeBuiltInWideAngleCamera,
    AVCaptureDeviceTypeContinuityCamera, AVCaptureDeviceTypeDeskViewCamera,
    AVCaptureDeviceTypeExternal, AVCaptureDeviceTypeMicrophone, AVMediaType, AVMediaTypeAudio,
    AVMediaTypeVideo,
};
use objc2_foundation::NSArray;

/// Whether a permission has been granted, refused, or not yet asked for.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Access {
    /// Granted. Capture will work.
    Granted,
    /// Never asked. macOS will prompt the first time.
    NotAsked,
    /// Refused, or blocked by policy. Capture will produce nothing.
    Denied,
    /// AVFoundation did not answer. Should not happen on a supported macOS, but saying
    /// "denied" when the question was never answered would be a lie.
    Unknown,
}

impl Access {
    pub fn granted(self) -> bool {
        self == Access::Granted
    }

    /// What this means for a recording, for `doctor` to print.
    pub fn explain(self) -> &'static str {
        match self {
            Access::Granted => "granted",
            Access::NotAsked => "not asked yet — macOS will prompt on the first recording",
            Access::Denied => "denied — grant it in System Settings › Privacy & Security",
            Access::Unknown => "could not be determined",
        }
    }
}

fn status(media: Option<&AVMediaType>) -> Access {
    let Some(media) = media else {
        return Access::Unknown;
    };
    // Safe: authorizationStatusForMediaType: is a class-level query. It reads the current
    // TCC decision and returns, with no prompt and no capture session involved.
    match unsafe { AVCaptureDevice::authorizationStatusForMediaType(media) } {
        AVAuthorizationStatus::Authorized => Access::Granted,
        AVAuthorizationStatus::NotDetermined => Access::NotAsked,
        _ => Access::Denied,
    }
}

/// Whether the microphone may be recorded.
pub fn microphone_access() -> Access {
    // Safe: reading a framework string constant.
    status(unsafe { AVMediaTypeAudio })
}

/// Whether the camera may be recorded.
pub fn camera_access() -> Access {
    // Safe: reading a framework string constant.
    status(unsafe { AVMediaTypeVideo })
}

/// One camera or microphone as `AVCaptureDevice` reports it.
pub struct Device {
    /// A stable identifier — not shown to a person, but what config matching resolves
    /// against internally and what would be handed to AVFoundation to open the device.
    pub id: String,
    /// What `recordo devices` prints and what `webcam.device` / `audio.device` match
    /// against, case-insensitively and by substring, the same way `--app` matches a
    /// window.
    pub name: String,
}

fn discover(
    types: &NSArray<objc2_av_foundation::AVCaptureDeviceType>,
    media: Option<&AVMediaType>,
) -> Vec<Device> {
    // Safe: a discovery session only enumerates already-connected hardware; it does not
    // open, activate, or request permission for anything.
    let session = unsafe {
        AVCaptureDeviceDiscoverySession::discoverySessionWithDeviceTypes_mediaType_position(
            types,
            media,
            AVCaptureDevicePosition::Unspecified,
        )
    };
    // Safe: `devices` reads the already-computed list.
    unsafe { session.devices() }
        .iter()
        .map(|d| Device {
            // Safe: uniqueID/localizedName are plain string accessors.
            id: unsafe { d.uniqueID() }.to_string(),
            name: unsafe { d.localizedName() }.to_string(),
        })
        .collect()
}

/// Every microphone AVFoundation can see, in no particular order beyond what macOS gives.
pub fn microphones() -> Vec<Device> {
    // Safe: constructing an NSArray of static framework string constants.
    let types = unsafe { NSArray::from_slice(&[AVCaptureDeviceTypeMicrophone]) };
    discover(&types, unsafe { AVMediaTypeAudio })
}

/// Every camera AVFoundation can see: built-in, Continuity Camera, Desk View, and
/// anything plugged in externally.
pub fn cameras() -> Vec<Device> {
    // Safe: constructing an NSArray of static framework string constants.
    let types = unsafe {
        NSArray::from_slice(&[
            AVCaptureDeviceTypeBuiltInWideAngleCamera,
            AVCaptureDeviceTypeContinuityCamera,
            AVCaptureDeviceTypeDeskViewCamera,
            AVCaptureDeviceTypeExternal,
        ])
    };
    discover(&types, unsafe { AVMediaTypeVideo })
}
