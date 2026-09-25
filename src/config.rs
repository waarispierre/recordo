//! User-editable settings, loaded from `recordo.toml` next to the project.
//!
//! Everything has a default, so a missing or partial file is fine — the file only needs
//! to name the values being overridden.

use crate::camera::CameraConfig;
use crate::render::{Chrome, Style};
use anyhow::{Context, Result};
use serde::{Deserialize, Serialize};
use std::path::{Path, PathBuf};

pub const DEFAULT_PATH: &str = "recordo.toml";

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
#[serde(default, deny_unknown_fields)]
pub struct Config {
    pub camera: CameraSettings,
    pub style: StyleSettings,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(default, deny_unknown_fields)]
pub struct CameraSettings {
    /// How far to zoom in, as a percentage. 0 disables zooming, 45 means 1.45x.
    pub zoom_percent: f64,
    pub zoom_in_s: f64,
    pub hold_s: f64,
    pub zoom_out_s: f64,
    /// Cursor-follow spring frequency in Hz; higher tracks more tightly.
    pub follow_hz: f64,
    /// Radius of the deadzone around the camera centre, as a percentage of the visible
    /// width. Cursor movement inside it is ignored. 0 follows continuously.
    pub follow_deadzone_percent: f64,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(default, deny_unknown_fields)]
pub struct StyleSettings {
    /// Gap between the background edge and the window, in points.
    pub padding: f32,
    pub corner_radius: f32,
    pub shadow_offset: [f32; 2],
    pub shadow_blur: f32,
    pub shadow_alpha: f32,
    /// Gradient endpoints, RGB 0-1. Ignored when `background_image` is set.
    pub bg_top: [f32; 3],
    pub bg_bottom: [f32; 3],
    /// Image behind the window, scaled to cover. Empty means use the gradient.
    pub background_image: String,
    /// "auto", "none", "window", or "browser".
    ///
    /// "auto" decides per recorded app: browsers get the mock browser frame with their
    /// own tab/address strip cropped away; native apps get none, since they already draw
    /// a title bar and adding another yields a window inside a window.
    pub chrome: String,
    pub chrome_bg: [f32; 3],
    pub pill_color: [f32; 3],
    /// Height in points of real browser chrome (tabs + address bar) to crop off before
    /// compositing. Varies by browser and whether a bookmarks bar is shown.
    pub browser_crop_top: f32,
}

impl Default for CameraSettings {
    fn default() -> Self {
        let d = CameraConfig::default();
        Self {
            zoom_percent: (d.max_zoom - 1.0) * 100.0,
            zoom_in_s: d.zoom_in_s,
            hold_s: d.hold_s,
            zoom_out_s: d.zoom_out_s,
            follow_hz: d.follow_hz,
            follow_deadzone_percent: d.follow_deadzone_percent,
        }
    }
}

impl Default for StyleSettings {
    fn default() -> Self {
        let d = Style::default();
        let rgb = |c: [f32; 4]| [c[0], c[1], c[2]];
        Self {
            padding: d.padding,
            corner_radius: d.corner_radius,
            shadow_offset: d.shadow_offset,
            shadow_blur: d.shadow_blur,
            shadow_alpha: d.shadow_alpha,
            bg_top: rgb(d.bg_top),
            bg_bottom: rgb(d.bg_bottom),
            background_image: String::new(),
            chrome: "auto".into(),
            chrome_bg: rgb(d.chrome_bg),
            pill_color: rgb(d.pill_color),
            browser_crop_top: 88.0,
        }
    }
}

impl Config {
    /// The background image, if one is set and the path resolves.
    ///
    /// Returns an error rather than silently falling back to the gradient: a typo in the
    /// path should be reported, not quietly ignored.
    pub fn background_path(&self) -> Result<Option<PathBuf>> {
        let raw = self.style.background_image.trim();
        if raw.is_empty() {
            return Ok(None);
        }
        let expanded = if let Some(rest) = raw.strip_prefix("~/") {
            dirs::home_dir()
                .context("could not locate your home directory")?
                .join(rest)
        } else {
            PathBuf::from(raw)
        };
        if !expanded.exists() {
            anyhow::bail!(
                "background_image {} does not exist — set it to \"\" to use the gradient",
                expanded.display()
            );
        }
        Ok(Some(expanded))
    }

    /// Loads the config, writing a commented default file if none exists yet.
    pub fn load_or_create(path: impl AsRef<Path>) -> Result<Self> {
        let path = path.as_ref();
        if !path.exists() {
            std::fs::write(path, DEFAULT_TOML)
                .with_context(|| format!("write default config to {}", path.display()))?;
            println!("wrote default config to {}", path.display());
            return Ok(Self::default());
        }
        let text =
            std::fs::read_to_string(path).with_context(|| format!("read {}", path.display()))?;
        toml::from_str(&text).with_context(|| format!("parse {}", path.display()))
    }

    pub fn camera(&self) -> CameraConfig {
        let c = &self.camera;
        CameraConfig {
            max_zoom: 1.0 + (c.zoom_percent.max(0.0) / 100.0),
            zoom_in_s: c.zoom_in_s.max(0.01),
            hold_s: c.hold_s.max(0.0),
            zoom_out_s: c.zoom_out_s.max(0.01),
            follow_hz: c.follow_hz.max(0.05),
            // Above 50% the boundary would exceed the visible frame and the camera
            // could never move at all.
            follow_deadzone_percent: c.follow_deadzone_percent.clamp(0.0, 50.0),
        }
    }

    /// Style for a specific recorded app. `chrome` may depend on which app it was.
    pub fn style(&self, bundle_id: Option<&str>) -> Style {
        let s = &self.style;
        let rgb = |c: [f32; 3]| [c[0], c[1], c[2], 1.0];
        Style {
            padding: s.padding.max(0.0),
            corner_radius: s.corner_radius.max(0.0),
            shadow_offset: s.shadow_offset,
            shadow_blur: s.shadow_blur.max(0.01),
            shadow_alpha: s.shadow_alpha.clamp(0.0, 1.0),
            bg_top: rgb(s.bg_top),
            bg_bottom: rgb(s.bg_bottom),
            chrome: self.resolve_chrome(bundle_id).0,
            chrome_bg: rgb(s.chrome_bg),
            pill_color: rgb(s.pill_color),
        }
    }

    /// Chrome mode plus how many points to crop off the top of the capture. The crop is
    /// nonzero only for browsers, where removing the real tab strip and address bar is
    /// what lets the drawn frame stand in for them — and is what strips tabs, bookmarks,
    /// profile avatar and URL history from the recording.
    pub fn resolve_chrome(&self, bundle_id: Option<&str>) -> (Chrome, f32) {
        let crop = self.style.browser_crop_top.max(0.0);
        match self.style.chrome.to_ascii_lowercase().as_str() {
            "none" => (Chrome::None, 0.0),
            "window" => (Chrome::Window, 0.0),
            "browser" => (Chrome::Browser, crop),
            _ => {
                if bundle_id.is_some_and(is_browser) {
                    (Chrome::Browser, crop)
                } else {
                    (Chrome::None, 0.0)
                }
            }
        }
    }
}

/// Bundle identifiers of browsers whose own chrome should be replaced.
pub fn is_browser(bundle_id: &str) -> bool {
    const BROWSERS: &[&str] = &[
        "com.apple.safari",
        "com.apple.safaritechnologypreview",
        "com.google.chrome",
        "com.google.chrome.canary",
        "com.microsoft.edgemac",
        "company.thebrowser.browser",
        "company.thebrowser.dia",
        "org.mozilla.firefox",
        "com.brave.browser",
        "com.vivaldi.vivaldi",
        "com.operasoftware.opera",
    ];
    let id = bundle_id.to_ascii_lowercase();
    BROWSERS
        .iter()
        .any(|b| id == *b || id.starts_with(&format!("{b}.")))
}

const DEFAULT_TOML: &str = r#"# recordo settings. Delete any line to fall back to its default.

[camera]
# How far to zoom in, as a percentage. 0 disables zooming, 45 means 1.45x.
zoom_percent = 45.0
# Seconds to ramp in, dwell, and ramp back out around each click.
zoom_in_s = 0.45
hold_s = 1.3
zoom_out_s = 0.7
# Cursor-follow spring frequency (Hz). Higher tracks more tightly, lower feels calmer.
follow_hz = 1.1

# How far the cursor may wander before the camera follows it, as a percentage of the
# visible width. The camera ignores everything inside this circle, so small movements
# while pointing or reading do not make it drift. Raise it for a calmer camera, set it
# to 0 to follow continuously.
follow_deadzone_percent = 10.0

[style]
# All lengths below are in POINTS and scale with the capture, so the look is the same
# on Retina and non-Retina displays.
padding = 48.0
# Roughly matches macOS's own window corners. Too small leaves dark crescents where the
# captured window's own rounded corners are not fully clipped.
corner_radius = 13.0
shadow_offset = [0.0, 9.0]
shadow_blur = 22.0
shadow_alpha = 0.45

# Background gradient, RGB 0-1. Ignored when background_image is set.
bg_top = [0.36, 0.40, 0.78]
bg_bottom = [0.60, 0.36, 0.72]

# Image behind the window, scaled to cover and centre-cropped. Empty uses the gradient
# above. Accepts ~ and relative paths.
background_image = ""

# Synthetic window frame: "auto", "none", "window", or "browser".
# "auto" gives browsers a mock browser frame with their real tab/address strip cropped
# off (no tabs, bookmarks, avatar or URL history), and leaves native apps alone since
# they already draw their own title bar.
chrome = "auto"
chrome_bg = [0.16, 0.16, 0.18]
pill_color = [0.24, 0.24, 0.27]

# Points of real browser chrome to crop. Raise if tabs still show, lower if page content
# is being cut off.
browser_crop_top = 88.0
"#;

/// Every setting as `section.key` with its current value, for `config show`/`get`.
pub fn flatten(path: &Path) -> Result<Vec<(String, String)>> {
    let text = std::fs::read_to_string(path).with_context(|| format!("read {}", path.display()))?;
    let doc: toml_edit::DocumentMut = text
        .parse()
        .with_context(|| format!("parse {}", path.display()))?;

    let mut out = Vec::new();
    for (section, item) in doc.iter() {
        match item.as_table() {
            Some(table) => {
                for (key, value) in table.iter() {
                    out.push((format!("{section}.{key}"), render_value(value)));
                }
            }
            None => out.push((section.to_string(), render_value(item))),
        }
    }
    Ok(out)
}

fn render_value(item: &toml_edit::Item) -> String {
    item.as_value()
        .map(|v| v.to_string().trim().to_string())
        .unwrap_or_default()
}

/// Writes one `section.key`, preserving the file's comments and layout.
pub fn set_value(path: &Path, key: &str, value: &str) -> Result<()> {
    let text = std::fs::read_to_string(path).with_context(|| format!("read {}", path.display()))?;
    let mut doc: toml_edit::DocumentMut = text
        .parse()
        .with_context(|| format!("parse {}", path.display()))?;

    let (section, field) = key
        .split_once('.')
        .with_context(|| format!("expected section.key, got {key:?}"))?;
    let table = doc
        .get_mut(section)
        .and_then(|i| i.as_table_mut())
        .with_context(|| format!("no such section: {section}"))?;
    if !table.contains_key(field) {
        anyhow::bail!("no such setting: {key}");
    }
    table[field] = toml_edit::Item::Value(parse_value(value)?);

    std::fs::write(path, doc.to_string()).with_context(|| format!("write {}", path.display()))?;
    Ok(())
}

/// Infers the TOML type from the text so `config set` needs no type annotations.
fn parse_value(raw: &str) -> Result<toml_edit::Value> {
    let text = raw.trim();
    if let Ok(b) = text.parse::<bool>() {
        return Ok(b.into());
    }
    if let Ok(i) = text.parse::<i64>() {
        return Ok(i.into());
    }
    if let Ok(f) = text.parse::<f64>() {
        return Ok(f.into());
    }
    if text.starts_with('[') {
        // Arrays (colours, shadow offsets) arrive as "[0.1, 0.2, 0.3]".
        let inner = text.trim_start_matches('[').trim_end_matches(']');
        let mut array = toml_edit::Array::new();
        for part in inner.split(',').filter(|p| !p.trim().is_empty()) {
            let p = part.trim();
            if let Ok(i) = p.parse::<i64>() {
                array.push(i);
            } else if let Ok(f) = p.parse::<f64>() {
                array.push(f);
            } else {
                array.push(p.trim_matches('"'));
            }
        }
        return Ok(array.into());
    }
    Ok(text.trim_matches('"').into())
}

/// One setting as shown in the interactive menu.
#[derive(Debug, Clone)]
pub struct Setting {
    /// `section.key`
    pub key: String,
    pub value: String,
    /// The comment lines above it in the file, joined into one sentence.
    pub help: String,
}

/// Settings with the file's own comments attached as help text.
///
/// The comments are read from the raw text rather than the parsed document: they are
/// already written for a human, so there is no second copy to keep in sync.
pub fn settings(path: &Path) -> Result<Vec<Setting>> {
    let text = std::fs::read_to_string(path).with_context(|| format!("read {}", path.display()))?;

    let mut out = Vec::new();
    let mut section = String::new();
    let mut pending: Vec<String> = Vec::new();

    for line in text.lines() {
        let trimmed = line.trim();
        if trimmed.is_empty() {
            pending.clear();
            continue;
        }
        if let Some(comment) = trimmed.strip_prefix('#') {
            pending.push(comment.trim().to_string());
            continue;
        }
        if trimmed.starts_with('[') {
            section = trimmed.trim_matches(['[', ']']).to_string();
            pending.clear();
            continue;
        }
        if let Some((key, value)) = trimmed.split_once('=') {
            out.push(Setting {
                key: format!("{section}.{}", key.trim()),
                value: value.trim().to_string(),
                help: pending.join(" "),
            });
            pending.clear();
        }
    }
    Ok(out)
}

/// A `[r, g, b]` setting parsed back into 0-255 components, for showing a swatch.
pub fn parse_rgb(value: &str) -> Option<(u8, u8, u8)> {
    let inner = value.trim().strip_prefix('[')?.strip_suffix(']')?;
    let parts: Vec<f32> = inner
        .split(',')
        .map(|p| p.trim().parse::<f32>())
        .collect::<Result<Vec<_>, _>>()
        .ok()?;
    if parts.len() != 3 {
        return None;
    }
    let to_byte = |v: f32| (v.clamp(0.0, 1.0) * 255.0).round() as u8;
    Some((to_byte(parts[0]), to_byte(parts[1]), to_byte(parts[2])))
}

/// Accepts `#RRGGBB`, `RRGGBB` or `r,g,b` (0-1 floats) and returns TOML array text.
///
/// Hex is what people actually have to hand — design tools, CSS, brand guidelines all
/// speak it, whereas three normalised floats have to be worked out by hand.
pub fn rgb_to_toml(input: &str) -> Option<String> {
    let text = input.trim();
    let hex = text.trim_start_matches('#');
    if hex.len() == 6 && hex.chars().all(|c| c.is_ascii_hexdigit()) {
        let component = |i: usize| u8::from_str_radix(&hex[i..i + 2], 16).ok();
        let (r, g, b) = (component(0)?, component(2)?, component(4)?);
        return Some(format!(
            "[{:.3}, {:.3}, {:.3}]",
            r as f32 / 255.0,
            g as f32 / 255.0,
            b as f32 / 255.0
        ));
    }
    // Already an array, or bare floats.
    let inner = text.trim_start_matches('[').trim_end_matches(']');
    let parts: Vec<f32> = inner
        .split(',')
        .map(|p| p.trim().parse::<f32>())
        .collect::<Result<Vec<_>, _>>()
        .ok()?;
    if parts.len() != 3 {
        return None;
    }
    Some(format!(
        "[{:.3}, {:.3}, {:.3}]",
        parts[0].clamp(0.0, 1.0),
        parts[1].clamp(0.0, 1.0),
        parts[2].clamp(0.0, 1.0)
    ))
}

pub fn is_colour_key(key: &str) -> bool {
    matches!(
        key,
        "style.bg_top" | "style.bg_bottom" | "style.chrome_bg" | "style.pill_color"
    )
}

#[cfg(test)]
mod colour_tests {
    use super::*;

    #[test]
    fn hex_round_trips_through_floats() {
        assert_eq!(
            rgb_to_toml("#FF0000").as_deref(),
            Some("[1.000, 0.000, 0.000]")
        );
        assert_eq!(
            rgb_to_toml("00ff00").as_deref(),
            Some("[0.000, 1.000, 0.000]")
        );
        assert_eq!(parse_rgb("[1.0, 0.0, 0.0]"), Some((255, 0, 0)));
    }

    #[test]
    fn accepts_float_triples_and_clamps() {
        assert_eq!(
            rgb_to_toml("0.5, 0.5, 0.5").as_deref(),
            Some("[0.500, 0.500, 0.500]")
        );
        assert_eq!(
            rgb_to_toml("[2.0, -1.0, 0.5]").as_deref(),
            Some("[1.000, 0.000, 0.500]")
        );
    }

    #[test]
    fn rejects_nonsense() {
        assert!(rgb_to_toml("purple").is_none());
        assert!(rgb_to_toml("#12345").is_none());
        assert!(parse_rgb("[1.0, 0.0]").is_none());
    }
}
