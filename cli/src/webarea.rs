//! Finds a browser's web content area via the Accessibility API.
//!
//! Cropping a fixed number of points off the top of a browser window is a guess: it
//! breaks when the bookmarks bar is toggled, differs per browser, and leaves a sliver of
//! chrome or eats page content. The accessibility tree reports the web view's exact
//! bounds instead, so the capture can be cropped to the page and nothing else.
//!
//! Requires Accessibility permission. Returns None when unavailable, so the caller can
//! fall back to the fixed crop.

use accessibility_sys::{
    AXIsProcessTrusted, AXUIElementCopyAttributeValue, AXUIElementCreateApplication,
    AXUIElementRef, AXUIElementSetAttributeValue, AXValueGetValue, AXValueRef,
    kAXChildrenAttribute, kAXPositionAttribute, kAXRoleAttribute, kAXSizeAttribute,
    kAXValueTypeCGPoint, kAXValueTypeCGSize, kAXWindowsAttribute,
};
use core_foundation::array::{CFArray, CFArrayGetTypeID, CFArrayRef};
use core_foundation::base::{CFGetTypeID, CFRelease, CFType, CFTypeRef, TCFType};
use core_foundation::string::{CFString, CFStringRef};
use core_graphics::geometry::{CGPoint, CGSize};

/// Screen-space rectangle in points.
#[derive(Debug, Clone, Copy)]
pub struct Rect {
    pub x: f64,
    pub y: f64,
    pub w: f64,
    pub h: f64,
}

fn copy_attr(element: AXUIElementRef, attr: &str) -> Option<CFType> {
    let name = CFString::new(attr);
    let mut value: CFTypeRef = std::ptr::null();
    let err = unsafe {
        AXUIElementCopyAttributeValue(
            element,
            name.as_concrete_TypeRef() as CFStringRef,
            &mut value,
        )
    };
    if err != 0 || value.is_null() {
        return None;
    }
    Some(unsafe { CFType::wrap_under_create_rule(value) })
}

fn role_of(element: AXUIElementRef) -> Option<String> {
    let value = copy_attr(element, kAXRoleAttribute)?;
    value.downcast::<CFString>().map(|s| s.to_string())
}

/// Borrows a CFType as a CFArray, but only if it really is one.
///
/// The Accessibility API returns whatever the target application chose to expose. Casting
/// a non-array to CFArrayRef aborts the process inside CoreFoundation's type validation,
/// and that is reachable from any third-party app this tool is pointed at.
fn as_array(value: &CFType) -> Option<CFArray<CFType>> {
    let ptr = value.as_CFTypeRef();
    if ptr.is_null() || unsafe { CFGetTypeID(ptr) } != unsafe { CFArrayGetTypeID() } {
        return None;
    }
    Some(unsafe { CFArray::wrap_under_get_rule(ptr as CFArrayRef) })
}

fn children_of(element: AXUIElementRef) -> Vec<AXUIElementRef> {
    let Some(value) = copy_attr(element, kAXChildrenAttribute) else {
        return Vec::new();
    };
    // AXChildren is a CFArray of AXUIElementRef, which has no TCFType impl here, so the
    // array is read as raw pointers and each child retained for the caller.
    let Some(array) = as_array(&value) else {
        return Vec::new();
    };
    array
        .iter()
        .map(|item| {
            let ptr = item.as_CFTypeRef() as AXUIElementRef;
            unsafe { core_foundation::base::CFRetain(ptr as CFTypeRef) };
            ptr
        })
        .collect()
}

fn frame_of(element: AXUIElementRef) -> Option<Rect> {
    let pos = copy_attr(element, kAXPositionAttribute)?;
    let size = copy_attr(element, kAXSizeAttribute)?;

    let mut point = CGPoint { x: 0.0, y: 0.0 };
    let mut extent = CGSize {
        width: 0.0,
        height: 0.0,
    };
    let ok_pos = unsafe {
        AXValueGetValue(
            pos.as_CFTypeRef() as AXValueRef,
            kAXValueTypeCGPoint,
            (&raw mut point).cast(),
        )
    };
    let ok_size = unsafe {
        AXValueGetValue(
            size.as_CFTypeRef() as AXValueRef,
            kAXValueTypeCGSize,
            (&raw mut extent).cast(),
        )
    };
    if !ok_pos || !ok_size || extent.width <= 0.0 || extent.height <= 0.0 {
        return None;
    }
    Some(Rect {
        x: point.x,
        y: point.y,
        w: extent.width,
        h: extent.height,
    })
}

/// Depth-first search for the largest `AXWebArea`, which is the rendered page.
fn find_web_area(element: AXUIElementRef, depth: usize, best: &mut Option<Rect>) {
    // Browser hierarchies are shallow; a bound stops a pathological tree from hanging.
    if depth > 12 {
        return;
    }
    if role_of(element).as_deref() == Some("AXWebArea") {
        if let Some(rect) = frame_of(element) {
            let area = rect.w * rect.h;
            if best.is_none_or(|b| area > b.w * b.h) {
                *best = Some(rect);
            }
        }
        // A web area can contain nested frames; the outermost is the one we want.
        return;
    }
    for child in children_of(element) {
        find_web_area(child, depth + 1, best);
        unsafe { CFRelease(child as CFTypeRef) };
    }
}

/// Whether this process may read other apps' accessibility trees.
pub fn is_trusted() -> bool {
    unsafe { AXIsProcessTrusted() }
}

/// Window frames as the accessibility tree reports them, for diagnosing disagreements
/// with ScreenCaptureKit.
pub fn ax_windows(pid: i32) -> Vec<(Rect, Option<String>)> {
    if !unsafe { AXIsProcessTrusted() } {
        return Vec::new();
    }
    let app = unsafe { AXUIElementCreateApplication(pid) };
    if app.is_null() {
        return Vec::new();
    }
    enable_chromium_accessibility(app);
    let mut out = Vec::new();
    if let Some(array) = copy_attr(app, kAXWindowsAttribute)
        .as_ref()
        .and_then(as_array)
    {
        for item in array.iter() {
            let w = item.as_CFTypeRef() as AXUIElementRef;
            if let Some(f) = frame_of(w) {
                out.push((f, role_of(w)));
            }
        }
    }
    unsafe { CFRelease(app as CFTypeRef) };
    out
}

/// Chromium-based browsers expose only a stub accessibility tree until an assistive
/// client asks for the full one. Setting AXManualAccessibility is the documented opt-in.
fn enable_chromium_accessibility(app: AXUIElementRef) {
    let name = CFString::new("AXManualAccessibility");
    let value = core_foundation::boolean::CFBoolean::true_value();
    unsafe {
        AXUIElementSetAttributeValue(
            app,
            name.as_concrete_TypeRef() as CFStringRef,
            value.as_CFTypeRef(),
        );
    }
}

/// Bounds of the page content for the window whose frame is `window`, in screen points.
///
/// The window frame is required rather than optional: an app commonly has several
/// windows, including ones parked on other Spaces, and picking the largest web area
/// across all of them can silently return a rect belonging to a window that is not being
/// recorded.
///
/// Returns None when Accessibility permission is missing, the app is not a browser, or no
/// matching web area is found.
pub fn web_content_rect(pid: i32, window: Rect) -> Option<Rect> {
    if !unsafe { AXIsProcessTrusted() } {
        return None;
    }
    let app = unsafe { AXUIElementCreateApplication(pid) };
    if app.is_null() {
        return None;
    }
    enable_chromium_accessibility(app);

    // Chromium builds its accessibility tree asynchronously once asked, so the first read
    // after opting in usually finds nothing. Safari answers on the first attempt.
    let mut best = None;
    for attempt in 0..8 {
        if attempt > 0 {
            std::thread::sleep(std::time::Duration::from_millis(120));
        }
        if let Some(array) = copy_attr(app, kAXWindowsAttribute)
            .as_ref()
            .and_then(as_array)
        {
            for item in array.iter() {
                let ax_window = item.as_CFTypeRef() as AXUIElementRef;
                // Match on geometry — AX exposes no window id to join on.
                let Some(f) = frame_of(ax_window) else {
                    continue;
                };
                let matches = (f.x - window.x).abs() <= 2.0
                    && (f.y - window.y).abs() <= 2.0
                    && (f.w - window.w).abs() <= 2.0
                    && (f.h - window.h).abs() <= 2.0;
                if !matches {
                    continue;
                }
                find_web_area(ax_window, 0, &mut best);
                break;
            }
        }
        if best.is_some() {
            break;
        }
    }
    unsafe { CFRelease(app as CFTypeRef) };

    // A web area outside its own window means the tree is stale or we matched the wrong
    // window; better to fall back than to crop to nonsense.
    best.filter(|r| {
        r.w > 1.0
            && r.h > 1.0
            && r.x >= window.x - 2.0
            && r.y >= window.y - 2.0
            && r.x + r.w <= window.x + window.w + 2.0
            && r.y + r.h <= window.y + window.h + 2.0
    })
}
