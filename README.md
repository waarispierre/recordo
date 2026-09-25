# recordo

[![CI](https://github.com/waarispierre/recordo/actions/workflows/ci.yml/badge.svg)](https://github.com/waarispierre/recordo/actions/workflows/ci.yml)
[![License](https://img.shields.io/badge/license-MIT%20OR%20Apache--2.0-blue.svg)](#licence)

Screen recordings with a cursor-following camera.

Records an app window or the whole display, then re-renders it with a smooth auto-zoom
that follows your cursor, rounded corners, a drop shadow and a styled background. For
browsers it keeps only the web page and draws a clean window frame around it, so the
video shows no tabs, bookmarks, profile avatar or URL history.

macOS 26 (Tahoe) or later. See [why 26 and not 15](CONTRIBUTING.md#getting-set-up).

## Install

```sh
git clone https://github.com/waarispierre/recordo.git
cd recordo
./install.sh
```

The installer checks prerequisites before building, so a missing dependency surfaces
immediately rather than four minutes into a compile. Then:

```sh
recordo doctor
```

You need [Rust](https://rustup.rs) to build and FFmpeg to render:

```sh
brew install ffmpeg
```

<details>
<summary>Why not Homebrew?</summary>

A tap would mean a second repository to maintain, a release process, and — because
Homebrew builds inside a sandbox that SwiftPM's own sandbox cannot nest within — a shim
that disabled a layer of build isolation. That is a poor trade this early for one binary
on one platform.

</details>

`doctor` checks permissions and tooling and prints where settings and videos live.

| Permission | Needed for | If denied |
|---|---|---|
| Screen Recording | capturing at all | recording fails with a clear error |
| Input Monitoring | click detection | records and pans, but never click-zooms |
| Accessibility | exact web page bounds | browser pages fall back to a fixed crop guess |

Screen Recording requires quitting and reopening your terminal after granting it.

## Use

Run it bare and it opens a full-screen app, like lazygit:

```sh
recordo
```

```
 recordo   record   settings   recordings
┌─ what to record ─────────────────────────────────────┐
│ ❯ entire display                                     │
│   Ghostty — ~/code/recordo (1001x815)          │
│   Safari — Psicle Web Versions (1496x932)            │
└──────────────────────────────────────────────────────┘
 j/k move · enter select · tab switch · q quit
```

`tab` cycles panes (or `1`/`2`/`3`), `j`/`k` move, `enter` acts, `q` quits. The settings
pane shows each option's explanation beside it and a colour swatch for colours. Recording
and rendering suspend the app and hand the terminal back, then return.

Everything is also available as plain subcommands, which is what runs in scripts and over
a pipe:

```sh
recordo record          # pick a window, record until you press Enter
recordo -a Safari       # record Safari
recordo -w 422          # record a specific window id
recordo -d              # record the whole display
recordo -s 10           # stop after 10 seconds
recordo -z 30           # 30% zoom for this run only
```

Recording stops on Enter or Ctrl-C. The finished video opens automatically; pass
`--no-open` to suppress that, or `--no-render` to capture without rendering.

Recordings go to `~/Movies/Recordo/<timestamp>/`, each folder holding the raw
capture, its sidecars and the finished `export.mp4`.

```sh
recordo windows         # list recordable windows and their ids
recordo list            # list past recordings
recordo render          # re-render the most recent recording
recordo render <path>   # re-render a specific one
```

`render` picks up config changes without needing a new capture.

## Settings

```sh
recordo config          # browse and edit interactively
```

This is the same settings pane the full-screen app shows, for when you only want the
settings.

Arrow keys to move, Enter to edit, Esc to leave. Each setting shows its own explanation
from the config file, and blank input keeps the current value.

```
? settings ›
❯ camera.zoom_percent     45.0
  camera.zoom_in_s        0.45
  style.padding           48.0
  style.chrome            "auto"
  open in $EDITOR
  reset to defaults
  done
```

Non-interactive forms, for scripts:

```sh
recordo config show
recordo config get camera.zoom_percent
recordo config set camera.zoom_percent 30
recordo config edit     # open in $EDITOR
recordo config reset
```

Edits preserve the file's comments. Values are validated on write and rolled back if
invalid, so a bad value is reported immediately rather than at render time — including a
`background_image` path that does not exist. Piping the output
(`recordo config | cat`) prints the settings instead of opening the menu.

### Colours

Colours are stored as `[r, g, b]` floats but **accepted as hex**, which is the form design
tools and brand guidelines actually give you:

```sh
recordo config set style.bg_top "#5C66C7"
```

`config show`, the menu and the full-screen app all draw a swatch in the colour itself, so
a triple of floats does not have to be imagined.

### Background image

`style.background_image` is a normal setting — set it in the app, the menu, or directly:

```sh
recordo config set style.background_image "~/Pictures/backdrop.jpg"
recordo config set style.background_image ""      # back to the gradient
```

The image is scaled to **cover** and centre-cropped, so any aspect ratio works.

| Setting | Meaning |
|---|---|
| `camera.zoom_percent` | how far to zoom in; `0` disables zooming, `45` means 1.45x |
| `camera.zoom_in_s` / `hold_s` / `zoom_out_s` | how long the zoom ramps, dwells and releases |
| `camera.follow_hz` | cursor-follow springiness; higher tracks more tightly |
| `style.padding` | how much background shows around the window |
| `style.corner_radius` | window corner rounding |
| `style.bg_top` / `bg_bottom` | background gradient, RGB 0-1 |
| `style.background_image` | image behind the window; overrides the gradient. `""` uses the gradient. Accepts `~` and relative paths |
| `style.chrome` | `auto`, `none`, `window` or `browser` |

All lengths are in **points** and scale with the capture, so the look is identical on
Retina and non-Retina displays.

### Window chrome

`chrome = "auto"` decides per recorded app:

- **Browsers** get a drawn macOS frame with traffic lights and a blank URL pill, and only
  the web page is kept. The page bounds come from the browser itself via the
  Accessibility API, so they adapt to a toggled bookmarks bar.
- **Native apps** get no added frame — they already draw a title bar, and adding another
  gives a window inside a window.

Without Accessibility permission, browsers fall back to cropping `style.browser_crop_top`
points off the top, which is only a guess. The renderer says so when it happens.

## Privacy

Everything stays on this machine. There is no account, no upload, no telemetry and no
network code — `scripts/check-local-only.sh` asserts that against the built binary on
every run, rather than leaving it as a promise.

```sh
cargo build --release && ./scripts/check-local-only.sh
```

The same check runs in CI, which you can reproduce locally with `go run ci/main.go`.

Recordings live in `~/Movies/Recordo/`, created `0700` with every file `0600`, so
other accounts on the machine cannot read them. The config directory is the same.

The cursor tap listens for **mouse events only** — no keyboard event type is registered,
so it cannot observe keystrokes even with Input Monitoring granted. Cursor position is
polled and needs no permission at all.

`capture.mp4` is the **unredacted** recording: for a browser it still contains the tab
strip and address bar that the render removes. Keeping it lets you re-render without
re-recording, but it does mean the raw version stays on disk:

```sh
recordo prune                  # drop raw captures, keep the rendered videos
recordo prune --older-than 7   # only recordings a week old or more
recordo prune --all            # remove recordings entirely
```

Without `--all` it only touches recordings that already have a render, so it cannot delete
the sole copy of anything. See [SECURITY.md](SECURITY.md) for the full picture,
including known limitations.

## Known limitations

- **Windows on another Space cannot be recorded.** macOS reports a bogus position for
  them, which would produce a black video, so the tool refuses with an explanation.
  Switch to that Space first.
- **Chromium browsers** (Edge, Chrome, Brave) only expose their accessibility tree on
  request, and not at all while parked on another Space. Safari is reliable.
- Rendering runs at roughly real time on a Retina display; there is little headroom at 4K.


## Licence

Dual-licensed under either of:

- MIT ([LICENSE-MIT](LICENSE-MIT))
- Apache License 2.0 ([LICENSE-APACHE](LICENSE-APACHE))

at your option.

### On FFmpeg

recordo **executes** `ffmpeg` as a separate process and never links against it, so
FFmpeg's GPL licence does not extend to this project. No FFmpeg binary or library is
bundled or redistributed here — you install it separately.

Video is encoded with `h264_videotoolbox`, the hardware encoder built into macOS, so
H.264 patent licensing is covered by Apple's operating system rather than by this
project.

`cargo deny check` runs in CI and fails the build if any dependency would introduce a
copyleft licence or link FFmpeg directly.

### Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Contributions are accepted under the same dual
licence.
