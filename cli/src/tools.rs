//! Locating the helper binaries this tool shells out to.
//!
//! This process holds Screen Recording, Accessibility and Input Monitoring grants. A
//! binary planted in an earlier `$PATH` entry would inherit all three, so helpers are
//! resolved against known system locations first and `$PATH` is only a last resort.

use anyhow::{Result, anyhow};
use std::path::PathBuf;

/// Directories searched before `$PATH`, in order.
const TRUSTED_DIRS: &[&str] = &[
    "/opt/homebrew/bin",
    "/usr/local/bin",
    "/opt/local/bin",
    "/usr/bin",
    "/bin",
];

/// Absolute path to `name`, preferring trusted directories over `$PATH`.
pub fn find(name: &str) -> Option<PathBuf> {
    for dir in TRUSTED_DIRS {
        let candidate = PathBuf::from(dir).join(name);
        if is_executable(&candidate) {
            return Some(candidate);
        }
    }
    // Fall back to $PATH so unusual installs still work, accepting the weaker guarantee.
    std::env::var_os("PATH").and_then(|paths| {
        std::env::split_paths(&paths)
            .map(|dir| dir.join(name))
            .find(|p| is_executable(p))
    })
}

pub fn require(name: &str) -> Result<PathBuf> {
    find(name).ok_or_else(|| anyhow!("{name} not found — try `brew install ffmpeg`"))
}

fn is_executable(path: &std::path::Path) -> bool {
    use std::os::unix::fs::PermissionsExt;
    std::fs::metadata(path)
        .map(|m| m.is_file() && m.permissions().mode() & 0o111 != 0)
        .unwrap_or(false)
}

/// Rejects paths that ffmpeg would read as flags.
///
/// ffmpeg has no `--` terminator, so an argument beginning with `-` is parsed as an
/// option rather than a filename.
pub fn check_not_option_like(label: &str, value: &str) -> Result<()> {
    if value.starts_with('-') {
        return Err(anyhow!(
            "{label} {value:?} starts with '-', which ffmpeg would read as an option"
        ));
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn rejects_option_like_paths() {
        assert!(check_not_option_like("output", "-f").is_err());
        assert!(check_not_option_like("output", "./-weird.mp4").is_ok());
        assert!(check_not_option_like("output", "/tmp/fine.mp4").is_ok());
    }

    #[test]
    fn finds_a_core_system_binary_by_absolute_path() {
        let found = find("date").expect("date should exist");
        assert!(found.is_absolute());
        assert!(found.exists());
    }
}
