//! Full-screen terminal app, in the spirit of lazygit.
//!
//! Recording and rendering deliberately *suspend* the TUI rather than drawing over it:
//! both already have good line-oriented output, and driving them from inside the draw
//! loop would mean reimplementing that output plus threading a terminal handle through
//! the recorder. Suspending is what lazygit does when it opens an editor.

use anyhow::{Context, Result};
use crossterm::event::{self, Event, KeyCode, KeyEvent, KeyEventKind, KeyModifiers};
use crossterm::execute;
use crossterm::terminal::{
    EnterAlternateScreen, LeaveAlternateScreen, disable_raw_mode, enable_raw_mode,
};
use ratatui::prelude::*;
use ratatui::widgets::{Block, Borders, Clear, List, ListItem, ListState, Paragraph, Wrap};
use recordo::config::{self, Config, Setting};
use recordo::recorder::Target;
use recordo::session::{self, Session};
use std::time::Duration;

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

/// What the event loop asks `main` to do after the TUI is torn down.
pub enum Action {
    Quit,
    Record(Target),
    Render(std::path::PathBuf),
    Open(std::path::PathBuf),
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
        for w in recordo::pick::capturable(&content) {
            labels.push(recordo::pick::label(&w));
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
    editing: Option<String>,
    status: String,
    config_path: std::path::PathBuf,
}

impl App {
    fn new() -> Result<Self> {
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
            status: "j/k move · enter select · tab switch · q quit".into(),
            config_path,
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
}

/// Runs the TUI until the user picks an action or quits.
pub fn run() -> Result<Action> {
    enable_raw_mode().context("enable raw mode")?;
    let mut stdout = std::io::stdout();
    execute!(stdout, EnterAlternateScreen).context("enter alternate screen")?;
    let backend = CrosstermBackend::new(stdout);
    let mut terminal = Terminal::new(backend).context("create terminal")?;

    let result = event_loop(&mut terminal);

    // Restore the terminal even if the loop failed, or the shell is left unusable.
    disable_raw_mode().ok();
    execute!(terminal.backend_mut(), LeaveAlternateScreen).ok();
    terminal.show_cursor().ok();
    result
}

// Concrete rather than generic over Backend: ratatui 0.30's associated error type is not
// Send + Sync, so it cannot flow through anyhow from a generic context.
type Tui = Terminal<CrosstermBackend<std::io::Stdout>>;

fn event_loop(terminal: &mut Tui) -> Result<Action> {
    let mut app = App::new()?;

    loop {
        terminal.draw(|f| draw(f, &mut app))?;

        if !event::poll(Duration::from_millis(200))? {
            continue;
        }
        let Event::Key(key) = event::read()? else {
            continue;
        };
        if key.kind != KeyEventKind::Press {
            continue;
        }

        if app.editing.is_some() {
            if let Some(action) = handle_edit_key(&mut app, key)? {
                return Ok(action);
            }
            continue;
        }
        if let Some(action) = handle_key(&mut app, key)? {
            return Ok(action);
        }
    }
}

fn handle_key(app: &mut App, key: KeyEvent) -> Result<Option<Action>> {
    match (key.code, key.modifiers) {
        (KeyCode::Char('q'), _) | (KeyCode::Esc, _) => return Ok(Some(Action::Quit)),
        (KeyCode::Char('c'), KeyModifiers::CONTROL) => return Ok(Some(Action::Quit)),
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
                return Ok(Some(Action::Render(s.dir.clone())));
            }
        }
        (KeyCode::Enter, _) => match app.tab {
            Tab::Record => {
                let index = app.window_state.selected().unwrap_or(0);
                let target = match app.windows.ids.get(index).copied().flatten() {
                    Some(id) => Target::Window(id),
                    None => Target::Display,
                };
                return Ok(Some(Action::Record(target)));
            }
            Tab::Settings => {
                if let Some(s) = app
                    .setting_state
                    .selected()
                    .and_then(|i| app.settings.get(i))
                {
                    app.editing = Some(String::new());
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
                    return Ok(Some(Action::Open(target)));
                }
            }
        },
        _ => {}
    }
    Ok(None)
}

fn handle_edit_key(app: &mut App, key: KeyEvent) -> Result<Option<Action>> {
    let Some(buffer) = app.editing.as_mut() else {
        return Ok(None);
    };
    match key.code {
        KeyCode::Esc => {
            app.editing = None;
            app.status = "cancelled".into();
        }
        KeyCode::Backspace => {
            buffer.pop();
        }
        KeyCode::Char(c) => buffer.push(c),
        KeyCode::Enter => {
            let value = buffer.trim().to_string();
            app.editing = None;
            if value.is_empty() {
                app.status = "unchanged".into();
                return Ok(None);
            }
            let Some(setting) = app
                .setting_state
                .selected()
                .and_then(|i| app.settings.get(i))
            else {
                return Ok(None);
            };
            let key_name = setting.key.clone();
            let previous = setting.value.clone();
            let value = if config::is_colour_key(&key_name) {
                match config::rgb_to_toml(&value) {
                    Some(v) => v,
                    None => {
                        app.status = format!("{value} is not a colour — try #5C66C7");
                        return Ok(None);
                    }
                }
            } else {
                value
            };

            // Write, verify, roll back. Same contract as the non-interactive path: an
            // invalid value must never be left in the file.
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
        }
        _ => {}
    }
    Ok(None)
}

fn draw(frame: &mut Frame, app: &mut App) {
    let chunks = Layout::vertical([
        Constraint::Length(3),
        Constraint::Min(5),
        Constraint::Length(3),
    ])
    .split(frame.area());

    draw_tabs(frame, chunks[0], app);
    match app.tab {
        Tab::Record => draw_record(frame, chunks[1], app),
        Tab::Settings => draw_settings(frame, chunks[1], app),
        Tab::Recordings => draw_recordings(frame, chunks[1], app),
    }
    draw_status(frame, chunks[2], app);

    if app.editing.is_some() {
        draw_edit_popup(frame, app);
    }
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
    frame.render_widget(
        Paragraph::new(Line::from(spans)).block(Block::default().borders(Borders::BOTTOM)),
        area,
    );
}

fn draw_record(frame: &mut Frame, area: Rect, app: &mut App) {
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
    frame.render_stateful_widget(list, area, &mut app.window_state);
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

fn draw_status(frame: &mut Frame, area: Rect, app: &App) {
    let keys = match app.tab {
        Tab::Record => "enter record · tab switch · q quit",
        Tab::Settings => "enter edit · tab switch · q quit",
        Tab::Recordings => "enter open · r re-render · tab switch · q quit",
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
    let Some(buffer) = app.editing.as_ref() else {
        return;
    };
    let Some(setting) = app
        .setting_state
        .selected()
        .and_then(|i| app.settings.get(i))
    else {
        return;
    };

    let area = centered(60, 30, frame.area());
    frame.render_widget(Clear, area);

    let hint = if config::is_colour_key(&setting.key) {
        "hex like #5C66C7, or r, g, b".to_string()
    } else {
        format!("now {}", setting.value)
    };
    let body = vec![
        Line::from(Span::styled(hint, Style::default().fg(Color::DarkGray))),
        Line::from(""),
        Line::from(vec![
            Span::styled("› ", Style::default().fg(Color::Cyan)),
            Span::styled(
                buffer.clone(),
                Style::default().add_modifier(Modifier::BOLD),
            ),
            Span::styled("█", Style::default().fg(Color::Cyan)),
        ]),
    ];
    frame.render_widget(
        Paragraph::new(body).wrap(Wrap { trim: true }).block(
            Block::default()
                .borders(Borders::ALL)
                .title(format!(" {} ", setting.key))
                .border_style(Style::default().fg(Color::Cyan)),
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
