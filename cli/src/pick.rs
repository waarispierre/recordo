//! Choosing which window to record.

use screencapturekit::prelude::*;

/// Windows worth offering as a recording target.
///
/// ScreenCaptureKit reports everything the window server knows about, which includes the
/// Dock, Control Center, the desktop backstop, autofill popovers and assorted agents.
/// Normal application windows sit on layer 0; anything else is a system overlay.
pub fn capturable(content: &SCShareableContent) -> Vec<SCWindow> {
    const SYSTEM_APPS: &[&str] = &[
        "com.apple.dock",
        "com.apple.controlcenter",
        "com.apple.notificationcenterui",
        "com.apple.wallpaper",
        "com.apple.WindowManager",
        "com.apple.screencaptureui",
    ];

    let mut out: Vec<SCWindow> = content
        .windows()
        .into_iter()
        .filter(|w| {
            let f = w.frame();
            if w.window_layer() != 0 || !w.is_on_screen() {
                return false;
            }
            if f.size.width < 400.0 || f.size.height < 300.0 {
                return false;
            }
            match w.owning_application() {
                Some(app) => {
                    let id = app.bundle_identifier().to_ascii_lowercase();
                    !SYSTEM_APPS.iter().any(|s| id == s.to_ascii_lowercase())
                }
                None => false,
            }
        })
        .collect();

    // Group by app, largest window first within each.
    out.sort_by(|a, b| {
        let name = |w: &SCWindow| {
            w.owning_application()
                .map(|x| x.application_name())
                .unwrap_or_default()
        };
        let area = |w: &SCWindow| w.frame().size.width * w.frame().size.height;
        name(a).cmp(&name(b)).then(
            area(b)
                .partial_cmp(&area(a))
                .unwrap_or(std::cmp::Ordering::Equal),
        )
    });
    out
}

pub fn label(w: &SCWindow) -> String {
    let app = w
        .owning_application()
        .map(|a| a.application_name())
        .unwrap_or_else(|| "?".into());
    let f = w.frame();
    let title = w.title().unwrap_or_default();
    let title = if title.is_empty() {
        String::new()
    } else {
        format!(" — {title}")
    };
    format!(
        "{app}{title} ({}x{})",
        f.size.width as i32, f.size.height as i32
    )
}
