//! recordo — screen recordings with a cursor-following camera.

mod app;

use anyhow::{Context, Result};
use clap::{Parser, Subcommand};
use owo_colors::OwoColorize;
use recordo::capture::recorder::{self, Target};
use recordo::config::Config;
use recordo::session::{self, Session};

#[derive(Parser)]
#[command(
    name = "recordo",
    version,
    about = "Screen recordings with a cursor-following camera",
    long_about = "Records an app window or the whole display, then re-renders it with a \
                  smooth auto-zoom that follows your cursor, rounded corners, a drop \
                  shadow and a styled background.",
    arg_required_else_help = false
)]
struct Cli {
    #[command(subcommand)]
    command: Option<Command>,

    /// Record this app (matched on name, largest window wins)
    #[arg(short, long, global = true)]
    app: Option<String>,

    /// Record this window id (see `recordo windows`)
    #[arg(short, long, global = true)]
    window: Option<u32>,

    /// Record the whole display
    #[arg(short, long, global = true)]
    display: bool,

    /// Stop after this many seconds instead of waiting for Enter
    #[arg(short, long, global = true)]
    seconds: Option<u64>,

    /// Zoom percentage for this run only, e.g. 30 (overrides config)
    #[arg(short, long, global = true)]
    zoom: Option<f64>,

    /// Record but do not render
    #[arg(long, global = true)]
    no_render: bool,

    /// Do not open the finished video
    #[arg(long, global = true)]
    no_open: bool,

    /// Record voice over for this run only (overrides config)
    #[arg(long, global = true, conflicts_with = "no_mic")]
    mic: bool,

    /// Skip voice over for this run only (overrides config)
    #[arg(long, global = true)]
    no_mic: bool,

    /// Show the webcam overlay for this run only (overrides config)
    #[arg(long, global = true, conflicts_with = "no_webcam")]
    webcam: bool,

    /// Skip the webcam overlay for this run only (overrides config)
    #[arg(long, global = true)]
    no_webcam: bool,
}

#[derive(Subcommand)]
enum Command {
    /// Record and render (the default)
    Record,
    /// Re-render a recording, picking up config changes
    Render {
        /// Recording folder; defaults to the most recent
        path: Option<String>,
    },
    /// List windows that can be recorded
    Windows,
    /// List cameras and microphones, and the names the config accepts
    Devices,
    /// Check permissions and tooling
    Doctor {
        /// Also report what page bounds each open browser exposes
        #[arg(short, long)]
        verbose: bool,
    },
    /// Read and write settings
    Config {
        #[command(subcommand)]
        action: Option<ConfigAction>,
    },
    /// List past recordings
    List,
    /// Delete raw captures, keeping the rendered videos
    Prune {
        /// Only recordings at least this many days old
        #[arg(long)]
        older_than: Option<u64>,
        /// Delete the rendered videos too — removes the recordings entirely
        #[arg(long)]
        all: bool,
        /// Do not ask for confirmation
        #[arg(short, long)]
        yes: bool,
    },
}

#[derive(Subcommand)]
enum ConfigAction {
    /// Browse and edit settings interactively (the default)
    Menu,
    /// Print every setting and its value
    Show,
    /// Read one setting, e.g. `camera.max_zoom`
    Get { key: String },
    /// Write one setting, e.g. `camera.max_zoom 1.6`
    Set { key: String, value: String },
    /// Print the config file path
    Path,
    /// Open the config in $EDITOR
    Edit,
    /// Restore defaults
    Reset,
}

fn main() {
    if let Err(e) = run() {
        eprintln!("\n{} {e:#}", "✗".red().bold());
        std::process::exit(1);
    }
}

fn run() -> Result<()> {
    let cli = Cli::parse();
    let target = if cli.display {
        Target::Display
    } else if let Some(id) = cli.window {
        Target::Window(id)
    } else if let Some(app) = cli.app.clone() {
        Target::App(app)
    } else {
        Target::Ask
    };

    match cli.command {
        // A bare `recordo` opens the full-screen app, like lazygit. Any targeting
        // flag means the user wants the one-shot path, and a non-tty always does.
        None if cli.app.is_none()
            && cli.window.is_none()
            && !cli.display
            && std::io::IsTerminal::is_terminal(&std::io::stdin()) =>
        {
            run_tui(&cli)
        }
        None | Some(Command::Record) => cmd_record(&cli, target),
        Some(Command::Render { ref path }) => cmd_render(&cli, path.clone()),
        Some(Command::Windows) => cmd_windows(),
        Some(Command::Devices) => cmd_devices(),
        Some(Command::Doctor { verbose }) => app::ui::doctor(verbose),
        Some(Command::List) => cmd_list(),
        Some(Command::Prune {
            older_than,
            all,
            yes,
        }) => cmd_prune(older_than, all, yes),
        Some(Command::Config { ref action }) => {
            cmd_config(action.as_ref().unwrap_or(&ConfigAction::Menu))
        }
    }
}

/// Drives the TUI, suspending it whenever a long-running job needs the plain terminal.
fn run_tui(cli: &Cli) -> Result<()> {
    loop {
        match app::tui::run()? {
            app::tui::Action::Quit => return Ok(()),
            app::tui::Action::Record(target) => {
                let outcome = cmd_record(cli, target);
                report_and_pause(outcome)?;
            }
            app::tui::Action::Render(dir) => {
                let outcome = cmd_render(cli, Some(dir.to_string_lossy().into_owned()));
                report_and_pause(outcome)?;
            }
            app::tui::Action::Open(path) => {
                std::process::Command::new("/usr/bin/open")
                    .arg(&path)
                    .status()?;
            }
        }
    }
}

/// Shows the result of a suspended job and waits, so output is not swallowed when the
/// full-screen app takes the terminal back.
fn report_and_pause(outcome: Result<()>) -> Result<()> {
    if let Err(e) = outcome {
        eprintln!("\n{} {e:#}", "✗".red().bold());
    }
    print!("\n  {} ", "press enter to return".bright_black());
    std::io::Write::flush(&mut std::io::stdout()).ok();
    let mut line = String::new();
    std::io::stdin().read_line(&mut line)?;
    Ok(())
}

fn cmd_record(cli: &Cli, target: Target) -> Result<()> {
    let mut config = Config::load_or_create(session::config_path()?)?;
    if let Some(z) = cli.zoom {
        config.camera.zoom_percent = z;
    }
    if cli.mic {
        config.audio.microphone = true;
    } else if cli.no_mic {
        config.audio.microphone = false;
    }
    if cli.webcam {
        config.webcam.enabled = true;
    } else if cli.no_webcam {
        config.webcam.enabled = false;
    }
    let session = Session::create()?;

    app::ui::banner();
    recorder::record(
        &session,
        &target,
        &config,
        app::ui::choose_window,
        |plan| {
            println!(
                "  {} {}",
                "recording".green().bold(),
                plan.label.bright_black()
            );
            println!(
                "  {} {}x{} at {}x",
                "         ".dimmed(),
                plan.capture_w,
                plan.capture_h,
                plan.scale
            );
            if let Some(mic) = &plan.microphone {
                println!("  {} {}", "voice over".dimmed(), mic.bright_black());
            }
            if let Some(cam) = &plan.webcam {
                println!("  {} {}", "webcam".dimmed(), cam.bright_black());
            }
            println!();
        },
        || app::ui::wait_for_stop(cli.seconds),
    )?;

    let health = recorder::health(&session)?;
    app::ui::health_report(&health);

    if cli.no_render {
        println!("  {} {}", "saved".cyan(), session.dir.display());
        return Ok(());
    }

    let out = session.export();
    app::ui::render(&session, &out, cli.zoom)?;
    println!("\n  🏁 {}", out.display().bold());

    if !cli.no_open {
        let _ = std::process::Command::new("/usr/bin/open")
            .arg(&out)
            .status();
    }
    Ok(())
}

fn cmd_render(cli: &Cli, path: Option<String>) -> Result<()> {
    let session = match path {
        Some(p) => Session::open(p)?,
        None => Session::latest()?,
    };
    println!(
        "  {} {}",
        "rendering".green().bold(),
        session.dir.display().bright_black()
    );
    let out = session.export();
    app::ui::render(&session, &out, cli.zoom)?;
    println!("\n  🏁 {}", out.display().bold());
    if !cli.no_open {
        let _ = std::process::Command::new("/usr/bin/open")
            .arg(&out)
            .status();
    }
    Ok(())
}

fn cmd_windows() -> Result<()> {
    use screencapturekit::prelude::*;
    let content = SCShareableContent::get().context("grant Screen Recording permission")?;
    let windows = recordo::capture::windows::capturable(&content);
    if windows.is_empty() {
        println!("  no windows found");
        return Ok(());
    }
    println!("\n  {:>9}  {}", "ID".bold(), "WINDOW".bold());
    for w in windows {
        println!(
            "  {:>9}  {}",
            w.window_id().to_string().cyan(),
            recordo::capture::windows::label(&w)
        );
    }
    println!(
        "\n  {}\n",
        "recordo -w <ID>    or    recordo -a <app name>".bright_black()
    );
    Ok(())
}

fn cmd_devices() -> Result<()> {
    use recordo::capture::devices;

    println!("\n  {}", "microphones".bold());
    let mics = devices::microphones();
    if mics.is_empty() {
        println!("  none found");
    }
    for d in &mics {
        println!("  {} {}", "·".bright_black(), d.name);
    }

    println!("\n  {}", "cameras".bold());
    let cams = devices::cameras();
    if cams.is_empty() {
        println!("  none found");
    }
    for d in &cams {
        println!("  {} {}", "·".bright_black(), d.name);
    }

    println!(
        "\n  {}\n",
        "recordo config set audio.device \"<name>\"    or    webcam.device".bright_black()
    );
    Ok(())
}

fn cmd_list() -> Result<()> {
    let dir = session::recordings_dir()?;
    let mut entries: Vec<_> = std::fs::read_dir(&dir)?
        .filter_map(|e| e.ok())
        .filter(|e| e.path().join("capture.mp4").exists())
        .collect();
    entries.sort_by_key(|e| e.file_name());
    if entries.is_empty() {
        println!("  no recordings yet — run {}", "recordo".cyan());
        return Ok(());
    }
    println!("\n  {}", dir.display().bright_black());
    for e in entries {
        let rendered = e.path().join("export.mp4").exists();
        println!(
            "  {}  {}",
            e.file_name().to_string_lossy().cyan(),
            if rendered {
                "rendered".green().to_string()
            } else {
                "raw only".yellow().to_string()
            }
        );
    }
    println!();
    Ok(())
}

fn cmd_prune(older_than: Option<u64>, all: bool, yes: bool) -> Result<()> {
    let sessions = session::all_sessions()?;
    let targets: Vec<_> = sessions
        .into_iter()
        .filter(|s| older_than.is_none_or(|d| s.age_days().is_some_and(|a| a >= d)))
        // Without --all, only touch recordings that have already been rendered, so
        // pruning can never destroy the only copy of something.
        .filter(|s| all || (s.has_export() && s.raw_bytes() > 0))
        .collect();

    if targets.is_empty() {
        println!("  nothing to prune");
        return Ok(());
    }

    let freed: u64 = targets
        .iter()
        .map(|s| {
            if all {
                dir_bytes(&s.dir)
            } else {
                s.raw_bytes()
            }
        })
        .sum();

    println!();
    for s in &targets {
        println!(
            "  {}  {}",
            s.dir
                .file_name()
                .unwrap_or_default()
                .to_string_lossy()
                .cyan(),
            if all {
                "delete entirely".red().to_string()
            } else {
                "drop raw capture".yellow().to_string()
            }
        );
    }
    println!(
        "\n  {} recording(s), {:.1} MB\n",
        targets.len(),
        freed as f64 / 1_048_576.0
    );
    if all {
        println!("  {} this removes the rendered videos too\n", "!".red());
    }

    if !yes {
        print!("  {} ", "proceed? [y/N]:".bold());
        std::io::Write::flush(&mut std::io::stdout()).ok();
        let mut line = String::new();
        std::io::stdin().read_line(&mut line)?;
        if !matches!(line.trim().to_ascii_lowercase().as_str(), "y" | "yes") {
            println!("  cancelled");
            return Ok(());
        }
    }

    for s in &targets {
        if all {
            std::fs::remove_dir_all(&s.dir)
                .with_context(|| format!("remove {}", s.dir.display()))?;
        } else {
            s.drop_raw()?;
        }
    }
    println!(
        "  {} freed {:.1} MB",
        "✓".green(),
        freed as f64 / 1_048_576.0
    );
    Ok(())
}

fn dir_bytes(dir: &std::path::Path) -> u64 {
    std::fs::read_dir(dir)
        .map(|entries| {
            entries
                .filter_map(|e| e.ok())
                .filter_map(|e| e.metadata().ok())
                .map(|m| m.len())
                .sum()
        })
        .unwrap_or(0)
}

fn cmd_config(action: &ConfigAction) -> Result<()> {
    let path = session::config_path()?;
    match action {
        ConfigAction::Path => println!("{}", path.display()),
        ConfigAction::Menu => {
            Config::load_or_create(&path)?;
            // Piped or redirected output cannot drive a menu; printing the settings is
            // the useful thing to do instead of failing.
            if std::io::IsTerminal::is_terminal(&std::io::stdin()) {
                app::ui::config_menu(&path)?;
            } else {
                return cmd_config(&ConfigAction::Show);
            }
        }
        ConfigAction::Show => {
            Config::load_or_create(&path)?;
            println!("\n  {}\n", path.display().bright_black());
            for (k, v) in recordo::config::flatten(&path)? {
                let swatch = recordo::config::parse_rgb(&v)
                    .map(|(r, g, b)| format!("\x1b[38;2;{r};{g};{b}m██\x1b[0m "))
                    .unwrap_or_default();
                println!("  {:<28} {swatch}{v}", k.cyan());
            }
            println!();
        }
        ConfigAction::Get { key } => {
            Config::load_or_create(&path)?;
            let found = recordo::config::flatten(&path)?
                .into_iter()
                .find(|(k, _)| k == key);
            match found {
                Some((_, v)) => println!("{v}"),
                None => anyhow::bail!("no such setting: {key}"),
            }
        }
        ConfigAction::Set { key, value } => {
            Config::load_or_create(&path)?;
            // Accept hex for colours here too, so the menu and the flag agree.
            let value = &if recordo::config::is_colour_key(key) {
                recordo::config::rgb_to_toml(value)
                    .with_context(|| format!("{value} is not a colour — try #5C66C7"))?
            } else {
                value.clone()
            };
            let before = std::fs::read_to_string(&path)?;
            recordo::config::set_value(&path, key, value)?;
            // Reload so an invalid value is reported now rather than at render time.
            let reloaded = Config::load_or_create(&path)
                .with_context(|| format!("{key} = {value} is not valid"))
                .and_then(|c| c.background_path().map(|_| ()));
            if let Err(e) = reloaded {
                std::fs::write(&path, before)?;
                return Err(e);
            }
            println!("  {} {} = {}", "✓".green(), key.cyan(), value.bold());
        }
        ConfigAction::Edit => {
            Config::load_or_create(&path)?;
            let editor = std::env::var("EDITOR").unwrap_or_else(|_| "nano".into());
            std::process::Command::new(editor).arg(&path).status()?;
        }
        ConfigAction::Reset => {
            std::fs::remove_file(&path).ok();
            Config::load_or_create(&path)?;
            println!("  {} restored defaults", "✓".green());
        }
    }
    Ok(())
}
