//! Terminal presentation: prompts, progress and diagnostics.

use anyhow::{Context, Result};
use indicatif::{ProgressBar, ProgressStyle};
use owo_colors::OwoColorize;
use recordo::capture::recorder::Health;
use recordo::render::export::CropSource;
use recordo::session::Session;
use screencapturekit::prelude::SCWindow;
use std::io::Write;
use std::sync::Arc;
use std::sync::atomic::{AtomicBool, Ordering};
use std::time::{Duration, Instant};

pub fn banner() {
    println!(
        "\n  {} {}",
        "recordo".bold(),
        env!("CARGO_PKG_VERSION").bright_black()
    );
}

/// Numbered picker. Kept deliberately plain so it works over ssh and in dumb terminals.
pub fn choose_window(windows: &[SCWindow]) -> Result<Option<SCWindow>> {
    if windows.is_empty() {
        println!("  no windows found — recording the whole display");
        return Ok(None);
    }

    println!("\n  {}\n", "what should I record?".bold());
    println!("   {}  entire display", "0".cyan());
    for (i, w) in windows.iter().enumerate() {
        println!(
            "  {:>2}  {}",
            (i + 1).to_string().cyan(),
            recordo::capture::windows::label(w)
        );
    }
    print!("\n  {} ", "choice [0]:".bold());
    std::io::stdout().flush().ok();

    let mut line = String::new();
    std::io::stdin().read_line(&mut line)?;
    let choice: usize = line.trim().parse().unwrap_or(0);
    if choice == 0 {
        return Ok(None);
    }
    windows
        .get(choice - 1)
        .cloned()
        .map(Some)
        .with_context(|| format!("no option {choice}"))
}

/// Blocks until the recording should stop: Enter, Ctrl-C, or a fixed duration.
pub fn wait_for_stop(seconds: Option<u64>) {
    let start = Instant::now();
    let done = Arc::new(AtomicBool::new(false));

    // Ctrl-C ends the recording rather than killing the process, so the capture is still
    // finalised and the sidecars written.
    let flag = Arc::clone(&done);
    let _ = ctrlc::set_handler(move || flag.store(true, Ordering::SeqCst));

    if seconds.is_none() {
        let flag = Arc::clone(&done);
        std::thread::spawn(move || {
            let mut s = String::new();
            let _ = std::io::stdin().read_line(&mut s);
            flag.store(true, Ordering::SeqCst);
        });
    }

    let spinner = ProgressBar::new_spinner();
    spinner.set_style(
        ProgressStyle::with_template("  {spinner:.red} {msg}")
            .unwrap()
            .tick_strings(&["●", "●", "◐", "◑", "◒", "◓"]),
    );
    let hint = match seconds {
        Some(n) => format!("stopping after {n}s"),
        None => "press Enter to stop".to_string(),
    };

    loop {
        let elapsed = start.elapsed();
        if done.load(Ordering::SeqCst) {
            break;
        }
        if seconds.is_some_and(|n| elapsed >= Duration::from_secs(n)) {
            break;
        }
        spinner.set_message(format!(
            "{:>02}:{:02}  {}",
            elapsed.as_secs() / 60,
            elapsed.as_secs() % 60,
            hint.bright_black()
        ));
        spinner.tick();
        std::thread::sleep(Duration::from_millis(90));
    }
    spinner.finish_and_clear();
    println!(
        "  {} {:.1}s captured",
        "✓".green(),
        start.elapsed().as_secs_f64()
    );
}

/// A block in the setting's own colour, so RGB triples can be read at a glance.
///
/// Truecolor escapes are emitted directly: most terminals support them, and one that does
/// not simply shows the block uncoloured rather than breaking the layout.
fn swatch(value: &str) -> String {
    match recordo::config::parse_rgb(value) {
        Some((r, g, b)) => format!("\x1b[38;2;{r};{g};{b}m██\x1b[0m "),
        None => String::new(),
    }
}

pub fn health_report(h: &Health) {
    let gap_ok = h.median_gap_ms.is_finite() && h.median_gap_ms < 100.0;
    println!(
        "  {} {} frames · {} cursor samples · {} clicks",
        "·".bright_black(),
        h.frames,
        h.cursor_samples,
        h.clicks
    );
    if !gap_ok && h.cursor_samples > 10 {
        println!(
            "  {} cursor timing is off by {:.0}ms — zoom may lag the pointer",
            "!".yellow(),
            h.median_gap_ms
        );
    }
    if !h.tap_installed {
        println!(
            "  {} clicks were not recorded — grant Input Monitoring for click-zoom",
            "!".yellow()
        );
    }
    if h.cursor_samples <= 1 {
        println!(
            "  {} the cursor never moved, so there is nothing to zoom toward",
            "!".yellow()
        );
    }
}

pub fn render(session: &Session, out: &std::path::Path, zoom: Option<f64>) -> Result<()> {
    let bar = ProgressBar::new_spinner();
    bar.set_style(
        ProgressStyle::with_template("  {spinner:.cyan} {msg}")
            .unwrap()
            .tick_strings(&["▰▱▱", "▰▰▱", "▰▰▰", "▱▰▰", "▱▱▰", "▱▱▱"]),
    );
    bar.set_message("rendering".to_string());
    bar.enable_steady_tick(Duration::from_millis(120));

    let result = recordo::render::export::run_with(
        &session.capture().to_string_lossy(),
        &out.to_string_lossy(),
        zoom,
    );
    bar.finish_and_clear();

    let report = result?;
    println!(
        "  {} {}x{} · {} frames · {:.1}x realtime",
        "·".bright_black(),
        report.output.0,
        report.output.1,
        report.frames,
        report.realtime_ratio()
    );
    if report.crop_source == CropSource::FixedGuess {
        println!(
            "  {} {} page bounds unavailable — cropped by a fixed guess, so tabs may show",
            "!".yellow(),
            report.app_name
        );
    }
    Ok(())
}

pub fn doctor(verbose: bool) -> Result<()> {
    use screencapturekit::prelude::SCShareableContent;

    println!("\n  {}\n", "recordo doctor".bold());

    let screen = SCShareableContent::get().is_ok();
    check(
        "Screen Recording",
        screen,
        "required — grant it to your terminal, then restart it",
    );

    let accessibility = recordo::capture::webarea::is_trusted();
    check(
        "Accessibility",
        accessibility,
        "optional — without it, browser pages are cropped by a fixed guess",
    );

    // The click tap installs only with Input Monitoring; recording still works without.
    // Report the absolute binary that will actually run, not merely "something named
    // ffmpeg is on $PATH".
    for name in ["ffmpeg", "ffprobe"] {
        match recordo::tools::find(name) {
            Some(p) => println!(
                "  {} {:<18} {}",
                "✓".green(),
                name,
                p.display().bright_black()
            ),
            None => check(name, false, "required to render — brew install ffmpeg"),
        }
    }

    println!(
        "\n  {} {}",
        "config".bright_black(),
        recordo::session::config_path()?.display()
    );
    println!(
        "  {} {}",
        "videos".bright_black(),
        recordo::session::recordings_dir()?.display()
    );

    if verbose {
        browser_report()?;
    } else {
        println!("  {}", "--verbose adds per-browser detail".bright_black());
    }
    println!();
    Ok(())
}

/// Shows what page bounds each open browser reports, which is what decides whether a
/// recording is cropped exactly or by the fixed fallback.
fn browser_report() -> Result<()> {
    use screencapturekit::prelude::SCShareableContent;

    println!("\n  {}", "browsers".bold());
    let content = SCShareableContent::get().context("grant Screen Recording permission")?;
    let mut found = false;

    for w in recordo::capture::windows::capturable(&content) {
        let Some(app) = w.owning_application() else {
            continue;
        };
        if !recordo::config::is_browser(&app.bundle_identifier()) {
            continue;
        }
        found = true;
        let f = w.frame();
        let window = recordo::capture::webarea::Rect {
            x: f.origin.x,
            y: f.origin.y,
            w: f.size.width,
            h: f.size.height,
        };
        print!("  {} {:<16}", "·".bright_black(), app.application_name());
        match recordo::capture::webarea::web_content_rect(app.process_id(), window) {
            Some(r) => println!(
                "page {:.0}x{:.0}, chrome {:.0}pt {}",
                r.w,
                r.h,
                r.y - f.origin.y,
                "exact".green()
            ),
            None => println!(
                "{}",
                "page bounds unavailable — will use the fixed crop".yellow()
            ),
        }
    }
    if !found {
        println!("  {} no browser windows open", "·".bright_black());
    }
    Ok(())
}

fn check(name: &str, ok: bool, hint: &str) {
    if ok {
        println!("  {} {:<18}", "✓".green(), name);
    } else {
        println!("  {} {:<18} {}", "✗".red(), name, hint.bright_black());
    }
}

/// Interactive settings browser: pick a setting, edit it, repeat.
pub fn config_menu(path: &std::path::Path) -> Result<()> {
    use dialoguer::theme::ColorfulTheme;
    use dialoguer::{Input, Select};

    let theme = ColorfulTheme::default();
    let mut cursor = 0usize;

    loop {
        let settings = recordo::config::settings(path)?;
        if settings.is_empty() {
            println!("  config is empty — try {}", "recordo config reset".cyan());
            return Ok(());
        }

        // Pad the key column so values line up and the list scans vertically.
        let width = settings.iter().map(|s| s.key.len()).max().unwrap_or(0);
        let mut items: Vec<String> = settings
            .iter()
            .map(|s| {
                format!(
                    "{:<width$}  {}{}",
                    s.key,
                    swatch(&s.value),
                    s.value.bright_black(),
                    width = width
                )
            })
            .collect();
        items.push(format!("{}", "open in $EDITOR".cyan()));
        items.push(format!("{}", "reset to defaults".yellow()));
        items.push(format!("{}", "done".green()));

        let choice = Select::with_theme(&theme)
            .with_prompt("settings")
            .items(&items)
            .default(cursor.min(items.len() - 1))
            .interact_opt()?;

        // Esc and Ctrl-C both mean "leave things as they are".
        let Some(choice) = choice else { return Ok(()) };
        cursor = choice;

        if choice == items.len() - 1 {
            return Ok(());
        }
        if choice == items.len() - 2 {
            std::fs::remove_file(path).ok();
            recordo::config::Config::load_or_create(path)?;
            println!("  {} restored defaults", "✓".green());
            cursor = 0;
            continue;
        }
        if choice == items.len() - 3 {
            let editor = std::env::var("EDITOR").unwrap_or_else(|_| "nano".into());
            std::process::Command::new(editor).arg(path).status()?;
            continue;
        }

        let setting = &settings[choice];
        if !setting.help.is_empty() {
            println!("\n  {}", setting.help.bright_black());
        }
        // Deliberately not pre-filling the field. dialoguer's initial text is not
        // reliably erasable with the Delete key macOS sends, which would leave you unable
        // to clear it. Showing the current value and treating blank as "keep" avoids the
        // problem entirely.
        let is_colour = recordo::config::is_colour_key(&setting.key);
        let prompt = if is_colour {
            format!(
                "{} (hex like #5C66C7, or r,g,b — blank keeps it)",
                setting.key
            )
        } else {
            format!("{} (now {}, blank keeps it)", setting.key, setting.value)
        };
        let entered: String = Input::with_theme(&theme)
            .with_prompt(prompt)
            .allow_empty(true)
            .interact_text()?;

        if entered.trim().is_empty() || entered.trim() == setting.value.trim() {
            continue;
        }

        // Colours are stored as float triples but accepted as hex, which is the form
        // people actually have on hand.
        let entered = if is_colour {
            match recordo::config::rgb_to_toml(&entered) {
                Some(converted) => converted,
                None => {
                    println!(
                        "  {} {} is not a colour — try #5C66C7 or 0.36, 0.40, 0.78",
                        "✗".red(),
                        entered.bold()
                    );
                    continue;
                }
            }
        } else {
            entered
        };

        // Write, then reload. An invalid value is rolled back rather than left in a file
        // that would fail at render time.
        let before = std::fs::read_to_string(path)?;
        recordo::config::set_value(path, &setting.key, &entered)?;
        match recordo::config::Config::load_or_create(path) {
            Ok(_) => println!(
                "  {} {} = {}",
                "✓".green(),
                setting.key.cyan(),
                entered.bold()
            ),
            Err(e) => {
                std::fs::write(path, before)?;
                // The root cause names the offending field and type; the outer context
                // is just the file path, which is not what went wrong.
                // toml's error spans several lines of source excerpt; the final line is
                // the actual complaint ("invalid type: string, expected f64").
                let full = e
                    .chain()
                    .last()
                    .map(|c| c.to_string())
                    .unwrap_or_else(|| e.to_string());
                let cause = full
                    .lines()
                    .rev()
                    .find(|l| !l.trim().is_empty())
                    .unwrap_or(&full)
                    .trim()
                    .to_string();
                println!(
                    "  {} {} rejected: {} — keeping {}",
                    "✗".red(),
                    entered.bold(),
                    cause,
                    setting.value.bold()
                );
            }
        }
    }
}
