//! Where recordings and settings live.
//!
//! A tool on PATH gets run from arbitrary directories, so nothing is written relative to
//! the working directory. Each recording gets its own timestamped folder holding the raw
//! capture, its sidecars and the finished video, which keeps them from drifting out of
//! sync with each other.

use anyhow::{Context, Result};
use std::os::unix::fs::{OpenOptionsExt, PermissionsExt};
use std::path::{Path, PathBuf};
use std::time::{SystemTime, UNIX_EPOCH};

/// Recordings are of your screen, so nothing here should be readable by other local
/// accounts. Directories are owner-only; files are created 0600 rather than chmod'ed
/// afterwards, so there is no window where the content exists at a laxer mode.
const DIR_MODE: u32 = 0o700;
const FILE_MODE: u32 = 0o600;

/// Creates a directory owner-only, tightening it if it already exists laxer.
fn create_private_dir(path: &Path) -> Result<()> {
    std::fs::create_dir_all(path).with_context(|| format!("create {}", path.display()))?;
    let mut perms = std::fs::metadata(path)?.permissions();
    if perms.mode() & 0o777 != DIR_MODE {
        perms.set_mode(DIR_MODE);
        std::fs::set_permissions(path, perms)
            .with_context(|| format!("restrict {}", path.display()))?;
    }
    Ok(())
}

/// Writes a file that only the owner can read.
pub fn write_private(path: &Path, contents: &[u8]) -> Result<()> {
    use std::io::Write;
    let mut file = std::fs::OpenOptions::new()
        .write(true)
        .create(true)
        .truncate(true)
        .mode(FILE_MODE)
        .open(path)
        .with_context(|| format!("write {}", path.display()))?;
    file.write_all(contents)?;
    // create(true) leaves the mode alone on an existing file, so enforce it explicitly.
    let mut perms = file.metadata()?.permissions();
    if perms.mode() & 0o777 != FILE_MODE {
        perms.set_mode(FILE_MODE);
        std::fs::set_permissions(path, perms)?;
    }
    Ok(())
}

/// Tightens a file produced by something other than us — ffmpeg's output, or
/// ScreenCaptureKit's capture — which is created with the default umask.
pub fn restrict(path: &Path) -> Result<()> {
    if !path.exists() {
        return Ok(());
    }
    let mut perms = std::fs::metadata(path)?.permissions();
    if perms.mode() & 0o777 != FILE_MODE {
        perms.set_mode(FILE_MODE);
        std::fs::set_permissions(path, perms)
            .with_context(|| format!("restrict {}", path.display()))?;
    }
    Ok(())
}

pub fn config_path() -> Result<PathBuf> {
    let dir = dirs::config_dir()
        .context("could not locate a config directory")?
        .join("recordo");
    create_private_dir(&dir)?;
    Ok(dir.join("config.toml"))
}

pub fn recordings_dir() -> Result<PathBuf> {
    let dir = dirs::home_dir()
        .context("could not locate your home directory")?
        .join("Movies")
        .join("Recordo");
    create_private_dir(&dir)?;
    Ok(dir)
}

/// One recording: raw capture, sidecars and rendered output in a single folder.
#[derive(Debug, Clone)]
pub struct Session {
    pub dir: PathBuf,
}

impl Session {
    pub fn create() -> Result<Self> {
        let stamp = timestamp();
        let dir = recordings_dir()?.join(&stamp);
        create_private_dir(&dir)?;
        Ok(Self { dir })
    }

    pub fn open(dir: impl AsRef<Path>) -> Result<Self> {
        let dir = dir.as_ref().to_path_buf();
        if !dir.join("capture.mp4").exists() {
            anyhow::bail!("{} does not contain a capture.mp4", dir.display());
        }
        Ok(Self { dir })
    }

    /// The most recent recording, for re-rendering without naming it.
    pub fn latest() -> Result<Self> {
        let mut entries: Vec<_> = std::fs::read_dir(recordings_dir()?)?
            .filter_map(|e| e.ok())
            .filter(|e| e.path().join("capture.mp4").exists())
            .collect();
        entries.sort_by_key(|e| e.file_name());
        let last = entries
            .last()
            .context("no recordings yet — run `recordo` to make one")?;
        Ok(Self { dir: last.path() })
    }

    pub fn capture(&self) -> PathBuf {
        self.dir.join("capture.mp4")
    }
    pub fn telemetry(&self) -> PathBuf {
        self.dir.join("telemetry.json")
    }
    pub fn frames(&self) -> PathBuf {
        self.dir.join("frames.json")
    }
    pub fn meta(&self) -> PathBuf {
        self.dir.join("meta.json")
    }
    pub fn export(&self) -> PathBuf {
        self.dir.join("export.mp4")
    }
    pub fn camera(&self) -> PathBuf {
        self.dir.join("camera.mp4")
    }
    pub fn camera_frames(&self) -> PathBuf {
        self.dir.join("camera_frames.json")
    }

    pub fn has_export(&self) -> bool {
        self.export().exists()
    }

    /// Bytes held by the raw capture and its sidecars — everything `prune` would remove.
    pub fn raw_bytes(&self) -> u64 {
        [
            self.capture(),
            self.telemetry(),
            self.frames(),
            self.camera(),
            self.camera_frames(),
        ]
        .iter()
        .filter_map(|p| std::fs::metadata(p).ok())
        .map(|m| m.len())
        .sum()
    }

    /// Deletes the raw capture and its sidecars, keeping the rendered video.
    ///
    /// The raw capture is the unredacted one: for a browser it still contains the tab
    /// strip, address bar and bookmarks that the render deliberately removes. Keeping it
    /// forever means the redaction only ever applied to the copy you share. `camera.mp4`,
    /// when present, is the most personal file in the folder — a raw recording of your
    /// face — so leaving it out here would be a privacy bug, not just an oversight.
    pub fn drop_raw(&self) -> Result<()> {
        for path in [
            self.capture(),
            self.telemetry(),
            self.frames(),
            self.camera(),
            self.camera_frames(),
        ] {
            if path.exists() {
                std::fs::remove_file(&path)
                    .with_context(|| format!("remove {}", path.display()))?;
            }
        }
        Ok(())
    }

    /// Every byte the folder holds, raw and rendered alike.
    pub fn bytes(&self) -> u64 {
        std::fs::read_dir(&self.dir)
            .map(|entries| {
                entries
                    .filter_map(|e| e.ok())
                    .filter_map(|e| e.metadata().ok())
                    .map(|m| m.len())
                    .sum()
            })
            .unwrap_or(0)
    }

    /// The part of the folder name after the timestamp, if it has been labelled.
    pub fn label(&self) -> Option<String> {
        let name = self.dir.file_name()?.to_str()?;
        // `YYYY-MM-DD_HH-MM-SS_label`: the stamp is the first two underscore-separated
        // fields, anything after that is the label.
        let rest = name.splitn(3, '_').nth(2)?;
        (!rest.is_empty()).then(|| rest.to_string())
    }

    /// Renames the folder to carry `label`, keeping its timestamp prefix. An empty label
    /// removes one that was there.
    ///
    /// The timestamp is deliberately not up for editing: [`age_days`](Self::age_days)
    /// reads a recording's age straight off the folder name, and `all_sessions` sorts by
    /// it, so a free-form name would quietly drop the recording out of `prune
    /// --older-than` and confuse which recording counts as the latest.
    pub fn set_label(&mut self, label: &str) -> Result<()> {
        let name = self
            .dir
            .file_name()
            .and_then(|n| n.to_str())
            .context("recording has no folder name")?;
        let stamp: Vec<&str> = name.splitn(3, '_').take(2).collect();
        let stamp = stamp.join("_");

        let label = sanitize_label(label);
        let renamed = if label.is_empty() {
            stamp
        } else {
            format!("{stamp}_{label}")
        };
        let target = self
            .dir
            .parent()
            .context("recording has no parent directory")?
            .join(&renamed);
        if target == self.dir {
            return Ok(());
        }
        if target.exists() {
            anyhow::bail!("{renamed} already exists");
        }
        std::fs::rename(&self.dir, &target)
            .with_context(|| format!("rename {} to {renamed}", self.dir.display()))?;
        self.dir = target;
        Ok(())
    }

    /// Moves the whole recording to the Trash, so a mistaken delete stays recoverable.
    pub fn trash(&self) -> Result<()> {
        use objc2_foundation::{NSFileManager, NSString, NSURL};

        let path = NSString::from_str(&self.dir.to_string_lossy());
        let url = NSURL::fileURLWithPath(&path);
        NSFileManager::defaultManager()
            .trashItemAtURL_resultingItemURL_error(&url, None)
            .map_err(|e| anyhow::anyhow!("could not move to Trash: {e}"))
    }

    /// Age in days, derived from the folder name rather than mtime, which re-rendering
    /// would otherwise reset.
    pub fn age_days(&self) -> Option<u64> {
        let name = self.dir.file_name()?.to_str()?;
        let stamp = name.split('_').next()?;
        let mut parts = stamp.split('-');
        let y: i64 = parts.next()?.parse().ok()?;
        let m: i64 = parts.next()?.parse().ok()?;
        let d: i64 = parts.next()?.parse().ok()?;
        let then = days_from_civil(y, m as u32, d as u32);
        let now_secs = SystemTime::now().duration_since(UNIX_EPOCH).ok()?.as_secs() as i64
            + local_utc_offset_secs();
        Some((now_secs.div_euclid(86_400) - then).max(0) as u64)
    }
}

/// Howard Hinnant's days-from-civil.
fn days_from_civil(y: i64, m: u32, d: u32) -> i64 {
    let y = if m <= 2 { y - 1 } else { y };
    let era = y.div_euclid(400);
    let yoe = y - era * 400;
    let mp = if m > 2 { m - 3 } else { m + 9 } as i64;
    let doy = (153 * mp + 2) / 5 + d as i64 - 1;
    let doe = yoe * 365 + yoe / 4 - yoe / 100 + doy;
    era * 146_097 + doe - 719_468
}

/// Every recording, oldest first.
pub fn all_sessions() -> Result<Vec<Session>> {
    let mut entries: Vec<_> = std::fs::read_dir(recordings_dir()?)?
        .filter_map(|e| e.ok())
        .filter(|e| e.path().is_dir())
        .collect();
    entries.sort_by_key(|e| e.file_name());
    Ok(entries
        .into_iter()
        .map(|e| Session { dir: e.path() })
        .collect())
}

/// Folder-safe form of a typed label. A rename lands in a filesystem path, so rather than
/// rejecting awkward input this reduces it to letters, digits and dashes — which also
/// stops `..` or a stray separator from climbing out of the recordings directory.
fn sanitize_label(label: &str) -> String {
    let mut out = String::with_capacity(label.len());
    for c in label.trim().chars() {
        if c.is_ascii_alphanumeric() {
            out.push(c);
        } else if !out.ends_with('-') {
            out.push('-');
        }
    }
    out.trim_matches('-').to_string()
}

fn timestamp() -> String {
    // Local civil time from the epoch, avoiding a date-library dependency for what is
    // only ever a folder name.
    let secs = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_secs())
        .unwrap_or(0) as i64;
    let local = secs + local_utc_offset_secs();
    let days = local.div_euclid(86_400);
    let tod = local.rem_euclid(86_400);
    let (y, m, d) = civil_from_days(days);
    format!(
        "{y:04}-{m:02}-{d:02}_{:02}-{:02}-{:02}",
        tod / 3600,
        (tod % 3600) / 60,
        tod % 60
    )
}

fn local_utc_offset_secs() -> i64 {
    // `date +%z` is the cheapest way to get the offset without pulling in a tz crate.
    // Absolute path: a planted `date` on $PATH would run with this process's
    // Screen Recording and Accessibility grants.
    std::process::Command::new("/bin/date")
        .arg("+%z")
        .output()
        .ok()
        .and_then(|o| String::from_utf8(o.stdout).ok())
        .and_then(|s| {
            let s = s.trim();
            let sign = if s.starts_with('-') { -1 } else { 1 };
            let h: i64 = s.get(1..3)?.parse().ok()?;
            let m: i64 = s.get(3..5)?.parse().ok()?;
            Some(sign * (h * 3600 + m * 60))
        })
        .unwrap_or(0)
}

/// Howard Hinnant's days-from-civil, inverted.
fn civil_from_days(z: i64) -> (i64, u32, u32) {
    let z = z + 719_468;
    let era = z.div_euclid(146_097);
    let doe = z.rem_euclid(146_097);
    let yoe = (doe - doe / 1460 + doe / 36_524 - doe / 146_096) / 365;
    let y = yoe + era * 400;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
    let mp = (5 * doy + 2) / 153;
    let d = (doy - (153 * mp + 2) / 5 + 1) as u32;
    let m = if mp < 10 { mp + 3 } else { mp - 9 } as u32;
    (if m <= 2 { y + 1 } else { y }, m, d)
}

#[cfg(test)]
mod tests {
    use super::civil_from_days;

    #[test]
    fn days_from_civil_inverts_civil_from_days() {
        for z in [0i64, 19_723, 19_782, 20_000] {
            let (y, m, d) = civil_from_days(z);
            assert_eq!(
                super::days_from_civil(y, m, d),
                z,
                "round trip failed for {z}"
            );
        }
    }

    #[test]
    fn epoch_and_known_dates_round_trip() {
        assert_eq!(civil_from_days(0), (1970, 1, 1));
        assert_eq!(civil_from_days(19_723), (2024, 1, 1));
        // A leap day, the case most likely to be off by one.
        assert_eq!(civil_from_days(19_782), (2024, 2, 29));
    }

    #[test]
    fn a_label_keeps_only_characters_a_folder_name_can_carry() {
        use super::sanitize_label;
        assert_eq!(sanitize_label("onboarding demo"), "onboarding-demo");
        assert_eq!(sanitize_label("  spaced  out  "), "spaced-out");
        assert_eq!(sanitize_label("v2.1 (final)"), "v2-1-final");
    }

    #[test]
    fn a_label_cannot_climb_out_of_the_recordings_directory() {
        use super::sanitize_label;
        // The label is pasted straight into a path, so separators and dot segments are
        // the cases that matter.
        assert_eq!(sanitize_label("../../etc/passwd"), "etc-passwd");
        assert_eq!(sanitize_label("/absolute"), "absolute");
        assert_eq!(sanitize_label(".."), "");
        assert_eq!(sanitize_label("///"), "");
    }

    #[test]
    fn a_labelled_folder_still_reports_its_timestamp_and_label() {
        use super::Session;
        use std::path::PathBuf;

        let plain = Session {
            dir: PathBuf::from("/tmp/2026-09-28_11-06-17"),
        };
        assert_eq!(plain.label(), None);

        let labelled = Session {
            dir: PathBuf::from("/tmp/2026-09-28_11-06-17_onboarding-demo"),
        };
        assert_eq!(labelled.label().as_deref(), Some("onboarding-demo"));
        // Age is read off the folder name, so labelling must not break it.
        assert!(labelled.age_days().is_some());
    }
}
