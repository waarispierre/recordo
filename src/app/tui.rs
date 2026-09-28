//! Full-screen terminal app, in the spirit of lazygit.
//!
//! Nothing leaves the alternate screen any more: a job draws its own progress here rather
//! than suspending the app for line-oriented output.
//!
//! The two halves of that get there differently. Rendering and compressing are ffmpeg and
//! wgpu work, so they run on a background thread and report through a [`Progress`] channel
//! the draw loop polls once per frame. Recording stays on the *main* thread — see
//! [`run_record`] — because tearing an `AVCaptureSession` down from a spawned thread hangs
//! forever; the draw loop runs inside the recorder's own `stop` callback instead.
//!
//! `/usr/bin/open` is cheap and one-shot enough (it launches a separate GUI app and
//! returns almost immediately) to call inline via `.spawn()` rather than routing it
//! through any kind of action/event type.

use anyhow::{Context, Result};
use crossterm::event::{self, Event, KeyCode, KeyEvent, KeyEventKind, KeyModifiers};
use crossterm::execute;
use crossterm::terminal::{
    EnterAlternateScreen, LeaveAlternateScreen, disable_raw_mode, enable_raw_mode,
};
use ratatui::prelude::*;
use ratatui::widgets::{Block, Borders, Clear, List, ListItem, ListState, Paragraph, Wrap};
use recordo::capture::recorder::{self, Target};
use recordo::capture::webcam::{PreviewFrame, PreviewSink};
use recordo::config::{self, Config, Setting};
use recordo::render::export;
use recordo::session::{self, Session};
use std::cell::RefCell;
use std::path::PathBuf;
use std::rc::Rc;
use std::sync::{Arc, Mutex, mpsc};
use std::thread;
use std::time::{Duration, Instant};

#[derive(PartialEq, Clone, Copy)]
enum Tab {
    Record,
    Settings,
    Recordings,
}

impl Tab {
    fn title(self) -> &'static str {
        match self {
            Tab::Record => "record",
            Tab::Settings => "settings",
            Tab::Recordings => "recordings",
        }
    }
    fn next(self) -> Self {
        match self {
            Tab::Record => Tab::Settings,
            Tab::Settings => Tab::Recordings,
            Tab::Recordings => Tab::Record,
        }
    }
    fn prev(self) -> Self {
        match self {
            Tab::Record => Tab::Recordings,
            Tab::Settings => Tab::Record,
            Tab::Recordings => Tab::Settings,
        }
    }
}

/// Per-run overrides that would otherwise come from CLI flags, so a bare `recordo --zoom
/// 30` still honours `--zoom` when a recording is started from the Record tab.
#[derive(Clone, Default)]
pub struct RecordOverrides {
    pub zoom: Option<f64>,
    pub mic: Option<bool>,
    pub webcam: Option<bool>,
    pub seconds: Option<u64>,
    pub no_render: bool,
    pub no_open: bool,
}

/// What an in-progress [`Edit`] popup will do with its buffer once confirmed.
enum EditKind {
    /// Editing a config setting selected in the Settings tab.
    Setting,
    /// Editing a target size in MB for the recording at this path.
    CompressSize(PathBuf),
}

struct Edit {
    kind: EditKind,
    buffer: String,
}

/// A background job's kind. Recording is deliberately *not* one of these: see
/// [`run_record`].
enum JobKind {
    Render,
    Compress,
}

/// One tick from a job thread to the draw loop.
enum Progress {
    /// Render only: (frames done, frames total).
    RenderTick(usize, usize),
    /// Any job: finished. `Ok` carries a multi-line summary; `Err` a formatted error.
    Done(Result<String, String>),
}

struct JobHandle {
    kind: JobKind,
    rx: mpsc::Receiver<Progress>,
    handle: thread::JoinHandle<()>,
    started: Instant,
    frames: Option<(usize, usize)>,
}

/// State for the capture that [`run_record`] is driving on the main thread.
struct Recording {
    started: Instant,
    /// Filled in by `record`'s `on_start` callback, which fires on this same thread once
    /// the stream is live — shared by cell rather than by value because the draw loop and
    /// that callback are both in flight inside `record`.
    label: Rc<RefCell<Option<String>>>,
    /// Present only when `webcam.enabled`: the sink the camera's preview tap publishes
    /// into, paired with the webcam settings this recording is actually using.
    preview: Option<(PreviewSink, config::WebcamSettings)>,
    stopping: bool,
}

struct Windows {
    labels: Vec<String>,
    ids: Vec<Option<u32>>,
}

fn load_windows() -> Windows {
    use screencapturekit::prelude::SCShareableContent;
    let mut labels = vec!["entire display".to_string()];
    let mut ids: Vec<Option<u32>> = vec![None];
    if let Ok(content) = SCShareableContent::get() {
        for w in recordo::capture::windows::capturable(&content) {
            labels.push(recordo::capture::windows::label(&w));
            ids.push(Some(w.window_id()));
        }
    }
    Windows { labels, ids }
}

struct App {
    tab: Tab,
    windows: Windows,
    window_state: ListState,
    settings: Vec<Setting>,
    setting_state: ListState,
    recordings: Vec<Session>,
    recording_state: ListState,
    editing: Option<Edit>,
    job: Option<JobHandle>,
    recording: Option<Recording>,
    /// Set by the Record tab's Enter key and picked up by the event loop, which — unlike
    /// the key handler — can hand the terminal to [`run_record`].
    pending_record: Option<Target>,
    /// A finished job's report, shown as a popup until the next keypress dismisses it.
    report: Option<String>,
    status: String,
    config_path: PathBuf,
    overrides: RecordOverrides,
}

impl App {
    fn new(overrides: RecordOverrides) -> Result<Self> {
        let config_path = session::config_path()?;
        Config::load_or_create(&config_path)?;
        let mut app = Self {
            tab: Tab::Record,
            windows: load_windows(),
            window_state: ListState::default(),
            settings: config::settings(&config_path)?,
            setting_state: ListState::default(),
            recordings: session::all_sessions().unwrap_or_default(),
            recording_state: ListState::default(),
            editing: None,
            job: None,
            recording: None,
            pending_record: None,
            report: None,
            status: "j/k move · enter select · tab switch · q quit".into(),
            config_path,
            overrides,
        };
        app.window_state.select(Some(0));
        app.setting_state.select(Some(0));
        app.recording_state.select(if app.recordings.is_empty() {
            None
        } else {
            Some(0)
        });
        Ok(app)
    }

    fn reload_settings(&mut self) -> Result<()> {
        self.settings = config::settings(&self.config_path)?;
        Ok(())
    }

    fn selected_len(&self) -> usize {
        match self.tab {
            Tab::Record => self.windows.labels.len(),
            Tab::Settings => self.settings.len(),
            Tab::Recordings => self.recordings.len(),
        }
    }

    fn state(&mut self) -> &mut ListState {
        match self.tab {
            Tab::Record => &mut self.window_state,
            Tab::Settings => &mut self.setting_state,
            Tab::Recordings => &mut self.recording_state,
        }
    }

    fn move_by(&mut self, delta: isize) {
        let len = self.selected_len();
        if len == 0 {
            return;
        }
        let current = self.state().selected().unwrap_or(0) as isize;
        let next = (current + delta).rem_euclid(len as isize) as usize;
        self.state().select(Some(next));
    }

    /// Renders the finished recording on a background thread, so the draw loop keeps
    /// running while ffmpeg works. Unlike the capture itself this is safe off the main
    /// thread — it is ffmpeg and wgpu, with no AVFoundation session to tear down.
    fn start_render_of(&mut self, session: Session, health: recorder::Health) {
        let zoom = self.overrides.zoom;
        let no_open = self.overrides.no_open;
        let (tx, rx) = mpsc::channel();
        let handle = thread::spawn(move || {
            let outcome: Result<String> = (|| {
                let out = session.export();
                let tx_render = tx.clone();
                let report = export::run_with(
                    &session.capture().to_string_lossy(),
                    &out.to_string_lossy(),
                    zoom,
                    Some(&|done, total| {
                        let _ = tx_render.send(Progress::RenderTick(done, total));
                    }),
                )?;
                if !no_open {
                    let _ = std::process::Command::new("/usr/bin/open")
                        .arg(&out)
                        .spawn();
                }
                Ok(summarize_record(&health, &report, &out))
            })();
            let _ = tx.send(Progress::Done(outcome.map_err(|e| format!("{e:#}"))));
        });
        self.job = Some(JobHandle {
            kind: JobKind::Render,
            rx,
            handle,
            started: Instant::now(),
            frames: None,
        });
        self.status = "rendering…".into();
    }

    fn start_render(&mut self, dir: PathBuf) {
        let zoom = self.overrides.zoom;
        let (tx, rx) = mpsc::channel();
        let handle = thread::spawn(move || {
            let outcome: Result<String> = (|| {
                let session = Session::open(&dir)?;
                let out = session.export();
                let tx_render = tx.clone();
                let report = export::run_with(
                    &session.capture().to_string_lossy(),
                    &out.to_string_lossy(),
                    zoom,
                    Some(&|done, total| {
                        let _ = tx_render.send(Progress::RenderTick(done, total));
                    }),
                )?;
                Ok(summarize_render(&report, &out))
            })();
            let _ = tx.send(Progress::Done(outcome.map_err(|e| format!("{e:#}"))));
        });
        self.job = Some(JobHandle {
            kind: JobKind::Render,
            rx,
            handle,
            started: Instant::now(),
            frames: None,
        });
        self.status = "rendering…".into();
    }

    fn start_compress(&mut self, dir: PathBuf, max_mb: f64) {
        let (tx, rx) = mpsc::channel();
        let handle = thread::spawn(move || {
            let outcome: Result<String> = (|| {
                let src = dir.join("export.mp4");
                let dst = dir.join("export-compressed.mp4");
                let max_bytes = (max_mb * 1_000_000.0) as u64;
                let report = export::compress_to_size(
                    &src.to_string_lossy(),
                    &dst.to_string_lossy(),
                    max_bytes,
                )?;
                Ok(summarize_compress(&report, max_mb, &dst))
            })();
            let _ = tx.send(Progress::Done(outcome.map_err(|e| format!("{e:#}"))));
        });
        self.job = Some(JobHandle {
            kind: JobKind::Compress,
            rx,
            handle,
            started: Instant::now(),
            frames: None,
        });
        self.status = "compressing…".into();
    }
}

/// What the CLI's `app::ui::health_report` + `render`/`compress` print, condensed into the
/// popup shown when a job finishes — that detail must not silently vanish now that nothing
/// prints to a plain terminal.
fn summarize_record(
    health: &recorder::Health,
    report: &export::Report,
    out: &std::path::Path,
) -> String {
    let mut lines = health_lines(health);
    lines.push(render_line(report));
    if report.crop_source == export::CropSource::FixedGuess {
        lines.push(format!(
            "{} page bounds unavailable — cropped by a fixed guess, so tabs may show",
            report.app_name
        ));
    }
    lines.push(format!("saved to {}", out.display()));
    lines.join("\n")
}

/// The cursor/click sanity check `app::ui::health_report` prints on the CLI path.
fn health_lines(health: &recorder::Health) -> Vec<String> {
    let mut lines = vec![format!(
        "{} frames · {} cursor samples · {} clicks",
        health.frames, health.cursor_samples, health.clicks
    )];
    let gap_ok = health.median_gap_ms.is_finite() && health.median_gap_ms < 100.0;
    if !gap_ok && health.cursor_samples > 10 {
        lines.push(format!(
            "cursor timing is off by {:.0}ms — zoom may lag the pointer",
            health.median_gap_ms
        ));
    }
    if !health.tap_installed {
        lines.push("clicks were not recorded — grant Input Monitoring for click-zoom".into());
    }
    if health.cursor_samples <= 1 {
        lines.push("the cursor never moved, so there is nothing to zoom toward".into());
    }
    lines
}

fn summarize_render(report: &export::Report, out: &std::path::Path) -> String {
    format!("{}\nsaved to {}", render_line(report), out.display())
}

fn render_line(report: &export::Report) -> String {
    format!(
        "{}x{} · {} frames · {:.1}x realtime{}{}",
        report.output.0,
        report.output.1,
        report.frames,
        report.realtime_ratio(),
        if report.audio { " · voice over" } else { "" },
        if report.webcam { " · webcam" } else { "" }
    )
}

fn summarize_compress(
    report: &export::CompressReport,
    max_mb: f64,
    dst: &std::path::Path,
) -> String {
    let mb = |b: u64| b as f64 / 1_000_000.0;
    let mut msg = if report.reencoded {
        format!(
            "{:.1} MB → {:.1} MB{}",
            mb(report.input_bytes),
            mb(report.output_bytes),
            report
                .video_bitrate_bps
                .map(|b| format!(" · {:.1} Mbps video", b as f64 / 1_000_000.0))
                .unwrap_or_default()
        )
    } else {
        format!("already under {max_mb:.0} MB — copied as-is")
    };
    if !report.fits() {
        msg.push_str(&format!(
            "\nstill {:.1} MB, over the {max_mb:.0} MB target — try a lower size",
            mb(report.output_bytes)
        ));
    }
    msg.push_str(&format!("\nsaved to {}", dst.display()));
    msg
}

/// Captures `target`, drawing the TUI from inside the recorder's own `stop` callback.
///
/// The capture deliberately runs on the main thread. Tearing an `AVCaptureSession` down
/// from a spawned thread hangs forever in `-[AVCaptureSession stopRunning]` (confirmed by
/// sampling: it waits inside `graphWillStopForSession:` and never returns), which left
/// webcam recordings with a `camera.mp4` but no sidecars. `record` already hands the
/// caller the whole "how long do we capture for" decision through `stop`, so the draw loop
/// lives in there and the threading matches the CLI path exactly.
///
/// Returns `true` if the user asked to quit the app outright.
fn run_record(terminal: &mut Tui, app: &mut App, target: Target) -> Result<bool> {
    let mut config = match Config::load_or_create(&app.config_path) {
        Ok(c) => c,
        Err(e) => {
            app.status = format!("could not load config: {e:#}");
            return Ok(false);
        }
    };
    if let Some(z) = app.overrides.zoom {
        config.camera.zoom_percent = z;
    }
    if let Some(mic) = app.overrides.mic {
        config.audio.microphone = mic;
    }
    if let Some(webcam) = app.overrides.webcam {
        config.webcam.enabled = webcam;
    }

    let session = match Session::create() {
        Ok(s) => s,
        Err(e) => {
            app.status = format!("could not start a recording: {e:#}");
            return Ok(false);
        }
    };

    let preview: Option<(PreviewSink, config::WebcamSettings)> = config
        .webcam
        .enabled
        .then(|| (Arc::new(Mutex::new(None)) as PreviewSink, config.webcam()));
    let preview_for_camera = preview.as_ref().map(|(sink, _)| Arc::clone(sink));
    let label = Rc::new(RefCell::new(None));

    app.recording = Some(Recording {
        started: Instant::now(),
        label: Rc::clone(&label),
        preview,
        stopping: false,
    });
    app.status = "recording…".into();

    let seconds = app.overrides.seconds;
    let mut quit = false;
    let mut draw_error = None;

    let result = recorder::record(
        &session,
        &target,
        &config,
        // The TUI resolves a concrete target before it ever gets here, so the chooser —
        // only invoked for `Target::Ask` — never runs.
        |_| Ok(None),
        |plan| {
            let mut parts = vec![plan.label.clone()];
            if let Some(mic) = &plan.microphone {
                parts.push(format!("mic: {mic}"));
            }
            if let Some(cam) = &plan.webcam {
                parts.push(format!("webcam: {cam}"));
            }
            *label.borrow_mut() = Some(parts.join(" · "));
        },
        || {
            let started = Instant::now();
            loop {
                if let Err(e) = terminal.draw(|f| draw(f, app)) {
                    draw_error = Some(e);
                    return;
                }
                if seconds.is_some_and(|s| started.elapsed().as_secs() >= s) {
                    return;
                }
                match event::poll(Duration::from_millis(100)) {
                    Ok(false) => continue,
                    Ok(true) => {}
                    Err(e) => {
                        draw_error = Some(e);
                        return;
                    }
                }
                let Ok(Event::Key(key)) = event::read() else {
                    continue;
                };
                if key.kind != KeyEventKind::Press {
                    continue;
                }
                match (key.code, key.modifiers) {
                    (KeyCode::Char('q'), _)
                    | (KeyCode::Esc, _)
                    | (KeyCode::Char('c'), KeyModifiers::CONTROL) => {
                        quit = true;
                    }
                    (KeyCode::Enter, _) | (KeyCode::Char('s'), _) => {}
                    _ => continue,
                }
                // Either way the capture ends here; the teardown below still runs, so the
                // recording is finalised rather than abandoned.
                if let Some(rec) = app.recording.as_mut() {
                    rec.stopping = true;
                }
                app.status = "stopping…".into();
                let _ = terminal.draw(|f| draw(f, app));
                return;
            }
        },
        preview_for_camera,
    );

    app.recording = None;
    if let Some(e) = draw_error {
        return Err(e.into());
    }

    match result.and_then(|_| recorder::health(&session)) {
        Ok(health) => {
            if quit {
                app.report = Some(format!("saved to {}", session.dir.display()));
            } else if app.overrides.no_render {
                app.report = Some(format!(
                    "{}\nsaved to {}",
                    health_lines(&health).join("\n"),
                    session.dir.display()
                ));
                app.recordings = session::all_sessions().unwrap_or_default();
            } else {
                app.start_render_of(session, health);
            }
        }
        Err(e) => app.report = Some(format!("failed: {e:#}")),
    }
    Ok(quit)
}

/// Runs the TUI until the user quits.
pub fn run(overrides: &RecordOverrides) -> Result<()> {
    enable_raw_mode().context("enable raw mode")?;
    let mut stdout = std::io::stdout();
    execute!(stdout, EnterAlternateScreen).context("enter alternate screen")?;
    let backend = CrosstermBackend::new(stdout);
    let mut terminal = Terminal::new(backend).context("create terminal")?;

    let result = event_loop(&mut terminal, overrides);

    // Restore the terminal even if the loop failed, or the shell is left unusable.
    disable_raw_mode().ok();
    execute!(terminal.backend_mut(), LeaveAlternateScreen).ok();
    terminal.show_cursor().ok();
    result
}

// Concrete rather than generic over Backend: ratatui 0.30's associated error type is not
// Send + Sync, so it cannot flow through anyhow from a generic context.
type Tui = Terminal<CrosstermBackend<std::io::Stdout>>;

fn event_loop(terminal: &mut Tui, overrides: &RecordOverrides) -> Result<()> {
    let mut app = App::new(overrides.clone())?;

    loop {
        drain_job(&mut app);
        terminal.draw(|f| draw(f, &mut app))?;

        // Started from the Record tab's key handler, which has no terminal to hand over.
        if let Some(target) = app.pending_record.take()
            && run_record(terminal, &mut app, target)?
        {
            return Ok(());
        }

        if !event::poll(Duration::from_millis(100))? {
            continue;
        }
        let Event::Key(key) = event::read()? else {
            continue;
        };
        if key.kind != KeyEventKind::Press {
            continue;
        }

        if app.report.is_some() {
            app.report = None;
            app.status = "j/k move · enter select · tab switch · q quit".into();
            continue;
        }
        if app.job.is_some() {
            if handle_job_key(&mut app, key) {
                return Ok(());
            }
            continue;
        }
        if app.editing.is_some() {
            handle_edit_key(&mut app, key)?;
            continue;
        }
        if handle_key(&mut app, key) {
            return Ok(());
        }
    }
}

/// Drains every pending [`Progress`] message, updating job state; when the job has
/// finished, joins its thread, publishes the report popup and refreshes the recordings
/// list (stale after any of the three job kinds).
fn drain_job(app: &mut App) {
    let Some(job) = app.job.as_mut() else {
        return;
    };
    let mut finished = None;
    while let Ok(p) = job.rx.try_recv() {
        match p {
            Progress::RenderTick(done, total) => job.frames = Some((done, total)),
            Progress::Done(result) => finished = Some(result),
        }
    }
    if let Some(result) = finished {
        let job = app.job.take().expect("job present, just matched above");
        let _ = job.handle.join();
        app.report = Some(match result {
            Ok(msg) => msg,
            Err(e) => format!("failed: {e}"),
        });
        app.recordings = session::all_sessions().unwrap_or_default();
        app.status = "press any key to dismiss".into();
    }
}

/// Handles a keypress while a render or compress job is running. Returns `true` if the
/// user asked to quit, having first joined the thread so quitting never leaves ffmpeg
/// writing into a half-finished file behind the app's back.
fn handle_job_key(app: &mut App, key: KeyEvent) -> bool {
    match (key.code, key.modifiers) {
        (KeyCode::Char('q'), _)
        | (KeyCode::Esc, _)
        | (KeyCode::Char('c'), KeyModifiers::CONTROL) => {
            app.status = "finishing up…".into();
            if let Some(job) = app.job.take() {
                let _ = job.handle.join();
            }
            true
        }
        _ => false,
    }
}

fn handle_key(app: &mut App, key: KeyEvent) -> bool {
    match (key.code, key.modifiers) {
        (KeyCode::Char('q'), _) | (KeyCode::Esc, _) => return true,
        (KeyCode::Char('c'), KeyModifiers::CONTROL) => return true,
        (KeyCode::Char('j') | KeyCode::Down, _) => app.move_by(1),
        (KeyCode::Char('k') | KeyCode::Up, _) => app.move_by(-1),
        (KeyCode::Tab, _) | (KeyCode::Char('l'), _) => app.tab = app.tab.next(),
        (KeyCode::BackTab, _) | (KeyCode::Char('h'), _) => app.tab = app.tab.prev(),
        (KeyCode::Char('1'), _) => app.tab = Tab::Record,
        (KeyCode::Char('2'), _) => app.tab = Tab::Settings,
        (KeyCode::Char('3'), _) => app.tab = Tab::Recordings,
        (KeyCode::Char('r'), _) if app.tab == Tab::Recordings => {
            if let Some(s) = app
                .recording_state
                .selected()
                .and_then(|i| app.recordings.get(i))
            {
                let dir = s.dir.clone();
                app.start_render(dir);
            }
        }
        (KeyCode::Char('c'), _) if app.tab == Tab::Recordings => {
            if let Some(s) = app
                .recording_state
                .selected()
                .and_then(|i| app.recordings.get(i))
            {
                if s.has_export() {
                    app.editing = Some(Edit {
                        kind: EditKind::CompressSize(s.dir.clone()),
                        buffer: String::new(),
                    });
                    app.status = "target size in MB — enter to compress, esc to cancel".into();
                } else {
                    app.status = "render first — nothing to compress yet".into();
                }
            }
        }
        (KeyCode::Enter, _) => match app.tab {
            Tab::Record => {
                let index = app.window_state.selected().unwrap_or(0);
                app.pending_record = Some(match app.windows.ids.get(index).copied().flatten() {
                    Some(id) => Target::Window(id),
                    None => Target::Display,
                });
            }
            Tab::Settings => {
                if let Some(s) = app
                    .setting_state
                    .selected()
                    .and_then(|i| app.settings.get(i))
                {
                    app.editing = Some(Edit {
                        kind: EditKind::Setting,
                        buffer: String::new(),
                    });
                    app.status = format!("editing {} — enter to save, esc to cancel", s.key);
                }
            }
            Tab::Recordings => {
                if let Some(s) = app
                    .recording_state
                    .selected()
                    .and_then(|i| app.recordings.get(i))
                {
                    let target = if s.has_export() {
                        s.export()
                    } else {
                        s.capture()
                    };
                    let _ = std::process::Command::new("/usr/bin/open")
                        .arg(&target)
                        .spawn();
                }
            }
        },
        _ => {}
    }
    false
}

fn handle_edit_key(app: &mut App, key: KeyEvent) -> Result<()> {
    match key.code {
        KeyCode::Esc => {
            app.editing = None;
            app.status = "cancelled".into();
        }
        KeyCode::Backspace => {
            if let Some(edit) = app.editing.as_mut() {
                edit.buffer.pop();
            }
        }
        KeyCode::Char(c) => {
            if let Some(edit) = app.editing.as_mut() {
                edit.buffer.push(c);
            }
        }
        KeyCode::Enter => {
            let Some(edit) = app.editing.take() else {
                return Ok(());
            };
            let value = edit.buffer.trim().to_string();
            if value.is_empty() {
                app.status = "unchanged".into();
                return Ok(());
            }
            match edit.kind {
                EditKind::Setting => handle_setting_edit(app, value)?,
                EditKind::CompressSize(dir) => match value.parse::<f64>() {
                    Ok(mb) if mb.is_finite() && mb > 0.0 => app.start_compress(dir, mb),
                    _ => {
                        app.status = format!("{value:?} is not a size in MB — try 25");
                    }
                },
            }
        }
        _ => {}
    }
    Ok(())
}

fn handle_setting_edit(app: &mut App, value: String) -> Result<()> {
    let Some(setting) = app
        .setting_state
        .selected()
        .and_then(|i| app.settings.get(i))
    else {
        return Ok(());
    };
    let key_name = setting.key.clone();
    let previous = setting.value.clone();
    let value = if config::is_colour_key(&key_name) {
        match config::rgb_to_toml(&value) {
            Some(v) => v,
            None => {
                app.status = format!("{value} is not a colour — try #5C66C7");
                return Ok(());
            }
        }
    } else {
        value
    };

    // Write, verify, roll back. Same contract as the non-interactive path: an invalid
    // value must never be left in the file.
    let before = std::fs::read_to_string(&app.config_path)?;
    match config::set_value(&app.config_path, &key_name, &value)
        .and_then(|_| Config::load_or_create(&app.config_path))
        .and_then(|c| c.background_path().map(|_| ()))
    {
        Ok(()) => {
            app.status = format!("{key_name} = {value}");
            app.reload_settings()?;
        }
        Err(e) => {
            std::fs::write(&app.config_path, before)?;
            let cause = e
                .chain()
                .last()
                .map(|c| c.to_string())
                .unwrap_or_else(|| e.to_string());
            let cause = cause
                .lines()
                .rev()
                .find(|l| !l.trim().is_empty())
                .unwrap_or("invalid");
            app.status = format!("rejected: {} — keeping {previous}", cause.trim());
        }
    }
    Ok(())
}

fn draw(frame: &mut Frame, app: &mut App) {
    draw_background(frame, frame.area());

    // Inset, so the backdrop reads as a frame around the app rather than as noise behind
    // it: ratatui widgets only paint the cells they actually write, so anything drawn
    // earlier shows through a panel's empty interior unless that panel is cleared first.
    let body = frame.area().inner(Margin::new(2, 1));
    let chunks = Layout::vertical([
        Constraint::Length(3),
        Constraint::Min(5),
        Constraint::Length(3),
    ])
    .split(body);
    for area in chunks.iter() {
        frame.render_widget(Clear, *area);
    }

    draw_tabs(frame, chunks[0], app);
    if app.recording.is_some() {
        draw_recording(frame, chunks[1], app);
    } else if app.job.is_some() {
        draw_job(frame, chunks[1], app);
    } else {
        match app.tab {
            Tab::Record => draw_record(frame, chunks[1], app),
            Tab::Settings => draw_settings(frame, chunks[1], app),
            Tab::Recordings => draw_recordings(frame, chunks[1], app),
        }
    }
    draw_status(frame, chunks[2], app);

    if app.editing.is_some() {
        draw_edit_popup(frame, app);
    }
    if app.report.is_some() {
        draw_report_popup(frame, app);
    }
}

/// A faint diagonal dot texture behind the whole app — drawn first, so every panel drawn
/// afterwards simply overwrites the cells it occupies. No image support is needed (or
/// added): terminals without a graphics protocol can't show one anyway.
fn draw_background(frame: &mut Frame, area: Rect) {
    let dim = Style::default().fg(Color::Rgb(42, 34, 58));
    let mut lines = Vec::with_capacity(area.height as usize);
    for y in 0..area.height {
        let mut s = String::with_capacity(area.width as usize);
        for x in 0..area.width {
            s.push(if (x as i32 + y as i32 * 2) % 6 == 0 {
                '·'
            } else {
                ' '
            });
        }
        lines.push(Line::from(Span::styled(s, dim)));
    }
    frame.render_widget(Paragraph::new(lines), area);
}

fn draw_tabs(frame: &mut Frame, area: Rect, app: &App) {
    let mut spans = vec![Span::styled(
        " recordo ",
        Style::default()
            .fg(Color::Magenta)
            .add_modifier(Modifier::BOLD),
    )];
    for tab in [Tab::Record, Tab::Settings, Tab::Recordings] {
        let style = if tab == app.tab {
            Style::default()
                .fg(Color::Black)
                .bg(Color::Cyan)
                .add_modifier(Modifier::BOLD)
        } else {
            Style::default().fg(Color::DarkGray)
        };
        spans.push(Span::styled(format!(" {} ", tab.title()), style));
        spans.push(Span::raw(" "));
    }
    if let Some(rec) = &app.recording {
        // ~1s on/off — a pulse fast enough to read as "live" without being distracting.
        let lit = (rec.started.elapsed().as_millis() / 500) % 2 == 0;
        spans.push(Span::styled(
            "●",
            Style::default().fg(if lit { Color::Red } else { Color::DarkGray }),
        ));
    }
    frame.render_widget(
        Paragraph::new(Line::from(spans)).block(Block::default().borders(Borders::BOTTOM)),
        area,
    );
}

fn draw_record(frame: &mut Frame, area: Rect, app: &mut App) {
    // The window list rarely fills the tab; give the slack to the wordmark rather than to
    // a box of empty rows.
    let list_rows = app.windows.labels.len() as u16 + 2;
    let (list_area, mark_area) = if area.height > list_rows + WORDMARK_ROWS {
        let parts = Layout::vertical([
            Constraint::Length(list_rows),
            Constraint::Min(WORDMARK_ROWS),
        ])
        .split(area);
        (parts[0], Some(parts[1]))
    } else {
        (area, None)
    };

    let items: Vec<ListItem> = app
        .windows
        .labels
        .iter()
        .map(|l| ListItem::new(l.as_str()))
        .collect();
    let list = List::new(items)
        .block(
            Block::default()
                .borders(Borders::ALL)
                .title(" what to record "),
        )
        .highlight_style(Style::default().fg(Color::Black).bg(Color::Cyan))
        .highlight_symbol("❯ ");
    frame.render_stateful_widget(list, list_area, &mut app.window_state);

    if let Some(mark_area) = mark_area {
        draw_wordmark(frame, mark_area);
    }
}

/// Rows the wordmark occupies: three of block type, a blank, and the tagline.
const WORDMARK_ROWS: u16 = 5;

/// A block-type "RECORDO", centred in whatever space the Record tab has left over.
fn draw_wordmark(frame: &mut Frame, area: Rect) {
    // One 3x3 glyph per letter, joined by a space — small enough to render identically in
    // any font, unlike the taller ASCII-art banners that need a specific aspect ratio.
    const GLYPHS: [[&str; 3]; 7] = [
        ["███", "█▀▄", "█ ▀"], // R
        ["███", "██ ", "███"], // E
        ["███", "█  ", "███"], // C
        ["███", "█ █", "███"], // O
        ["███", "█▀▄", "█ ▀"], // R
        ["██ ", "█ █", "██ "], // D
        ["███", "█ █", "███"], // O
    ];
    const TAGLINE: &str = "screen recordings with a cursor-following camera";

    let width = GLYPHS.len() as u16 * 4 - 1;
    if area.width < width.max(TAGLINE.len() as u16) || area.height < WORDMARK_ROWS {
        return;
    }

    let mut lines: Vec<Line> = (0..3)
        .map(|row| {
            let text: Vec<&str> = GLYPHS.iter().map(|g| g[row]).collect();
            Line::from(Span::styled(
                text.join(" "),
                Style::default().fg(Color::Rgb(96, 68, 132)),
            ))
            .centered()
        })
        .collect();
    lines.push(Line::from(""));
    lines.push(
        Line::from(Span::styled(
            TAGLINE,
            Style::default().fg(Color::Rgb(70, 60, 88)),
        ))
        .centered(),
    );

    // Centre vertically too, so the mark sits in the middle of the free space.
    let top = area.y + (area.height.saturating_sub(WORDMARK_ROWS)) / 2;
    let placed = Rect {
        x: area.x,
        y: top,
        width: area.width,
        height: WORDMARK_ROWS.min(area.height),
    };
    frame.render_widget(Paragraph::new(lines), placed);
}

fn draw_settings(frame: &mut Frame, area: Rect, app: &mut App) {
    let panes =
        Layout::horizontal([Constraint::Percentage(62), Constraint::Percentage(38)]).split(area);

    let width = app.settings.iter().map(|s| s.key.len()).max().unwrap_or(0);
    let items: Vec<ListItem> = app
        .settings
        .iter()
        .map(|s| {
            let mut spans = vec![Span::raw(format!("{:<width$}  ", s.key, width = width))];
            // A colour is far easier to judge as a block than as three floats.
            if let Some((r, g, b)) = config::parse_rgb(&s.value) {
                spans.push(Span::styled(
                    "██ ",
                    Style::default().fg(Color::Rgb(r, g, b)),
                ));
            }
            spans.push(Span::styled(
                s.value.clone(),
                Style::default().fg(Color::DarkGray),
            ));
            ListItem::new(Line::from(spans))
        })
        .collect();

    let list = List::new(items)
        .block(Block::default().borders(Borders::ALL).title(" settings "))
        .highlight_style(Style::default().fg(Color::Black).bg(Color::Cyan))
        .highlight_symbol("❯ ");
    frame.render_stateful_widget(list, panes[0], &mut app.setting_state);

    let help = app
        .setting_state
        .selected()
        .and_then(|i| app.settings.get(i))
        .map(|s| s.help.clone())
        .unwrap_or_default();
    frame.render_widget(
        Paragraph::new(help)
            .wrap(Wrap { trim: true })
            .style(Style::default().fg(Color::Gray))
            .block(
                Block::default()
                    .borders(Borders::ALL)
                    .title(" what it does "),
            ),
        panes[1],
    );
}

fn draw_recordings(frame: &mut Frame, area: Rect, app: &mut App) {
    let items: Vec<ListItem> = app
        .recordings
        .iter()
        .map(|s| {
            let name = s
                .dir
                .file_name()
                .unwrap_or_default()
                .to_string_lossy()
                .to_string();
            let (label, colour) = if s.has_export() {
                ("rendered", Color::Green)
            } else {
                ("raw only", Color::Yellow)
            };
            ListItem::new(Line::from(vec![
                Span::raw(format!("{name}  ")),
                Span::styled(label, Style::default().fg(colour)),
            ]))
        })
        .collect();

    let list = if items.is_empty() {
        List::new(vec![ListItem::new(
            "no recordings yet — press 1 to make one",
        )])
    } else {
        List::new(items)
    };
    frame.render_stateful_widget(
        list.block(Block::default().borders(Borders::ALL).title(" recordings "))
            .highlight_style(Style::default().fg(Color::Black).bg(Color::Cyan))
            .highlight_symbol("❯ "),
        area,
        &mut app.recording_state,
    );
}

/// The panel shown while a capture is in flight: elapsed time, what is being recorded,
/// and — when the webcam is on — a live thumbnail in the configured corner.
fn draw_recording(frame: &mut Frame, area: Rect, app: &App) {
    let Some(rec) = app.recording.as_ref() else {
        return;
    };
    let block = Block::default().borders(Borders::ALL).title(" recording ");
    let inner = block.inner(area);
    frame.render_widget(block, area);

    let elapsed = rec.started.elapsed();
    let mut lines = vec![Line::from(vec![
        Span::styled("● ", Style::default().fg(Color::Red)),
        Span::raw(format!(
            "{:>02}:{:02}",
            elapsed.as_secs() / 60,
            elapsed.as_secs() % 60
        )),
    ])];
    if let Some(label) = rec.label.borrow().as_ref() {
        lines.push(Line::from(Span::styled(
            label.clone(),
            Style::default().fg(Color::DarkGray),
        )));
    }
    lines.push(Line::from(Span::styled(
        if rec.stopping {
            "stopping — finishing the file"
        } else {
            "enter/s stop · q stop and quit"
        },
        Style::default().fg(Color::DarkGray),
    )));
    frame.render_widget(Paragraph::new(lines), inner);

    if let Some((sink, cfg)) = &rec.preview {
        let thumb = sink.lock().ok().and_then(|g| g.clone());
        if let Some(thumb) = thumb {
            draw_webcam_pip(frame, inner, cfg, &thumb);
        }
    }
}

/// Replaces the tab's own content area with a status panel while a job is running —
/// simpler than threading job state through each of the three tab-specific draw
/// functions, and a job's progress matters regardless of which tab the user is looking at.
fn draw_job(frame: &mut Frame, area: Rect, app: &App) {
    let Some(job) = app.job.as_ref() else {
        return;
    };
    let block = Block::default().borders(Borders::ALL).title(" working ");
    let inner = block.inner(area);
    frame.render_widget(block, area);

    match &job.kind {
        JobKind::Render => {
            let (done, total) = job.frames.unwrap_or((0, 0));
            draw_render_progress(frame, inner, done, total);
        }
        JobKind::Compress => {
            const GLYPHS: [&str; 6] = ["▱▱▱", "▰▱▱", "▰▰▱", "▰▰▰", "▱▰▰", "▱▱▰"];
            let i = (job.started.elapsed().as_millis() / 120) as usize % GLYPHS.len();
            let lines = vec![Line::from("compressing"), Line::from(GLYPHS[i])];
            frame.render_widget(Paragraph::new(lines), inner);
        }
    }
}

/// The render bar. `total` of 0 means the first frame hasn't landed yet, which is a
/// distinct state from 0/N — ffmpeg is still starting up and there is nothing to show.
fn draw_render_progress(frame: &mut Frame, area: Rect, done: usize, total: usize) {
    let lines = if total == 0 {
        vec![Line::from("rendering"), Line::from("starting ffmpeg…")]
    } else {
        let width = 24usize;
        let filled = ((done as f64 / total as f64 * width as f64).round() as usize).min(width);
        let bar = format!("{}{}", "▰".repeat(filled), "▱".repeat(width - filled));
        vec![
            Line::from("rendering"),
            Line::from(format!("{bar}  {done}/{total} frames")),
        ]
    };
    frame.render_widget(Paragraph::new(lines), area);
}

/// Floats a small live webcam thumbnail in one corner of `area`, positioned, sized and
/// shaped the same way the export's picture-in-picture is configured — reusing
/// [`recordo::pip::pip_rect`] with terminal-cell dimensions in place of output pixels,
/// rather than reinventing the corner math.
fn draw_webcam_pip(
    frame: &mut Frame,
    area: Rect,
    cfg: &config::WebcamSettings,
    thumb: &PreviewFrame,
) {
    if area.width < 8 || area.height < 5 || thumb.w == 0 || thumb.h == 0 {
        return;
    }
    let corner = recordo::pip::Corner::parse(&cfg.position);
    // A cell is about twice as tall as it is wide, and the half-block trick fits two
    // source rows into each one, so a frame needs double the columns to not look squashed.
    let aspect = thumb.w as f32 / thumb.h as f32 * 2.0;
    // The configured percentage is authored against an exported frame of a thousand-odd
    // pixels; in a couple of dozen terminal rows the same number is too small to frame a
    // face by, so it only steers within a range that stays legible.
    let size_percent = cfg.size_percent.clamp(20.0, 40.0);
    let r = recordo::pip::pip_rect(
        area.width as f32,
        area.height as f32,
        corner,
        1.0,
        (0.0, 0.0),
        size_percent,
        aspect,
    );

    let x = area.x + (r.x.round() as u16).min(area.width.saturating_sub(4));
    let y = area.y + (r.y.round() as u16).min(area.height.saturating_sub(3));
    let width = (r.w.round() as u16)
        .max(4)
        .min(area.width.saturating_sub(x - area.x));
    let height = (r.h.round() as u16)
        .max(3)
        .min(area.height.saturating_sub(y - area.y));
    let box_area = Rect {
        x,
        y,
        width,
        height,
    };

    frame.render_widget(Clear, box_area);
    let circle = cfg.shape == "circle";
    let round_corners = !circle && cfg.corner_radius > 0.0;
    let lines = render_half_blocks(
        thumb,
        box_area.width,
        box_area.height,
        circle,
        round_corners,
        cfg.mirror,
    );
    frame.render_widget(Paragraph::new(lines), box_area);
}

/// Renders `thumb` as `cols`x`rows` terminal cells using the half-block trick (`▀`, fg =
/// top source pixel, bg = bottom), doubling the effective vertical resolution without
/// needing an image-in-terminal crate. `circle` masks cells outside the box's inscribed
/// ellipse; `round_corners` is a coarse approximation of `corner_radius` — clipping the
/// four outer cells — since character cells can't do sub-cell rounding.
fn render_half_blocks(
    thumb: &PreviewFrame,
    cols: u16,
    rows: u16,
    circle: bool,
    round_corners: bool,
    mirror: bool,
) -> Vec<Line<'static>> {
    let cols = cols.max(1);
    let rows = rows.max(1);
    let sample = |sx: u32, sy: u32| -> (u8, u8, u8) {
        let sx = sx.min(thumb.w.saturating_sub(1));
        let sy = sy.min(thumb.h.saturating_sub(1));
        let i = ((sy * thumb.w + sx) * 3) as usize;
        (thumb.rgb[i], thumb.rgb[i + 1], thumb.rgb[i + 2])
    };

    let mut lines = Vec::with_capacity(rows as usize);
    for row in 0..rows {
        let mut spans = Vec::with_capacity(cols as usize);
        for col in 0..cols {
            let corner_cell =
                round_corners && (col == 0 || col == cols - 1) && (row == 0 || row == rows - 1);
            let masked = corner_cell
                || (circle && {
                    let cx = (col as f32 + 0.5) / cols as f32 - 0.5;
                    let cy = (row as f32 + 0.5) / rows as f32 - 0.5;
                    cx * cx + cy * cy > 0.25
                });
            if masked {
                spans.push(Span::raw(" "));
                continue;
            }

            let nx = (col as f32 + 0.5) / cols as f32;
            let nx = if mirror { 1.0 - nx } else { nx };
            let sx = (nx * thumb.w as f32) as u32;
            let sy_top = ((row as f32 * 2.0 + 0.5) / (rows as f32 * 2.0) * thumb.h as f32) as u32;
            let sy_bot = ((row as f32 * 2.0 + 1.5) / (rows as f32 * 2.0) * thumb.h as f32) as u32;
            let (r1, g1, b1) = sample(sx, sy_top);
            let (r2, g2, b2) = sample(sx, sy_bot);
            spans.push(Span::styled(
                "▀",
                Style::default()
                    .fg(Color::Rgb(r1, g1, b1))
                    .bg(Color::Rgb(r2, g2, b2)),
            ));
        }
        lines.push(Line::from(spans));
    }
    lines
}

fn draw_status(frame: &mut Frame, area: Rect, app: &App) {
    let keys = match app.tab {
        Tab::Record => "enter record · tab switch · q quit",
        Tab::Settings => "enter edit · tab switch · q quit",
        Tab::Recordings => "enter open · r re-render · c compress · tab switch · q quit",
    };
    let line = Line::from(vec![
        Span::styled(
            format!(" {} ", app.status),
            Style::default().fg(Color::White),
        ),
        Span::styled(format!("  {keys}"), Style::default().fg(Color::DarkGray)),
    ]);
    frame.render_widget(
        Paragraph::new(line).block(Block::default().borders(Borders::TOP)),
        area,
    );
}

fn draw_edit_popup(frame: &mut Frame, app: &App) {
    let Some(edit) = app.editing.as_ref() else {
        return;
    };

    let (title, hint) = match &edit.kind {
        EditKind::Setting => {
            let Some(setting) = app
                .setting_state
                .selected()
                .and_then(|i| app.settings.get(i))
            else {
                return;
            };
            let hint = if config::is_colour_key(&setting.key) {
                "hex like #5C66C7, or r, g, b".to_string()
            } else {
                format!("now {}", setting.value)
            };
            (setting.key.clone(), hint)
        }
        EditKind::CompressSize(dir) => {
            let name = dir
                .file_name()
                .unwrap_or_default()
                .to_string_lossy()
                .to_string();
            (
                format!("compress {name}"),
                "target size in MB, e.g. 25".to_string(),
            )
        }
    };

    let area = centered(60, 30, frame.area());
    frame.render_widget(Clear, area);

    let body = vec![
        Line::from(Span::styled(hint, Style::default().fg(Color::DarkGray))),
        Line::from(""),
        Line::from(vec![
            Span::styled("› ", Style::default().fg(Color::Cyan)),
            Span::styled(
                edit.buffer.clone(),
                Style::default().add_modifier(Modifier::BOLD),
            ),
            Span::styled("█", Style::default().fg(Color::Cyan)),
        ]),
    ];
    frame.render_widget(
        Paragraph::new(body).wrap(Wrap { trim: true }).block(
            Block::default()
                .borders(Borders::ALL)
                .title(format!(" {title} "))
                .border_style(Style::default().fg(Color::Cyan)),
        ),
        area,
    );
}

fn draw_report_popup(frame: &mut Frame, app: &App) {
    let Some(report) = app.report.as_ref() else {
        return;
    };
    let area = centered(64, 50, frame.area());
    frame.render_widget(Clear, area);

    let lines: Vec<Line> = report.lines().map(|l| Line::from(l.to_string())).collect();
    frame.render_widget(
        Paragraph::new(lines).wrap(Wrap { trim: true }).block(
            Block::default()
                .borders(Borders::ALL)
                .title(" done — press any key ")
                .border_style(Style::default().fg(Color::Green)),
        ),
        area,
    );
}

fn centered(percent_x: u16, percent_y: u16, area: Rect) -> Rect {
    let vertical = Layout::vertical([
        Constraint::Percentage((100 - percent_y) / 2),
        Constraint::Percentage(percent_y),
        Constraint::Percentage((100 - percent_y) / 2),
    ])
    .split(area);
    Layout::horizontal([
        Constraint::Percentage((100 - percent_x) / 2),
        Constraint::Percentage(percent_x),
        Constraint::Percentage((100 - percent_x) / 2),
    ])
    .split(vertical[1])[1]
}
