# Code tour

A guide to this codebase: what each part does, why it is shaped that way, and the handful
of things that look odd until you know the reason — all anchored to real code. Read it
alongside the source; every named function is real, so grep for it.

---

## 1. What the app does

Two phases, joined by files on disk:

```
  PHASE 1: record                        PHASE 2: render
  ───────────────                        ───────────────
  ScreenCaptureKit ──► capture.mp4    ┐
  (+ voice over, muxed in)            │
  frame timestamps ──► frames.json    ├──► camera solve ──► GPU composite ──► export.mp4
  cursor + clicks  ──► telemetry.json │         ▲
  window geometry  ──► meta.json      ┘         │
  AVFoundation     ──► camera.mp4   ────────────┘  (webcam overlay, own timestamps)
                   ──► camera_frames.json
```

`capture.mp4` is the **raw** window recording — real tabs, no styling. `export.mp4` is the
finished video with the auto-zoom, rounded corners, shadow and background.

Phase 2 never needs phase 1 to re-run. Change a setting, re-render, done. That separation
is the main architectural decision in the project.

## 2. The one idea that makes it work

macOS timestamps captured video frames and mouse events with **the same clock** —
`mach_absolute_time`. That is the linchpin.

ScreenCaptureKit gives no per-frame cursor information, so the cursor is captured
completely separately from the video. Because both carry mach timestamps, they can be
lined up afterwards: for any frame you can ask "where was the cursor at that instant?" and
get a real answer.

`internal/platform/clock` is the whole of that conversion. `record.CheckHealth` exists to
prove the alignment held on a given recording — a median gap of a few milliseconds is
healthy; if it is ever seconds, the design is void.

## 3. Repo layout: vertical slices

The code is organised **by feature, not by technical layer**. A slice owns its use case,
its CLI command and its tab in the full-screen app, so changing a feature means touching
one folder.

```
cmd/recordo/          main: assembles the root command from the slices
internal/features/
  record/             what to capture, capturing it, sidecars, health
                        record.go   the use case (MakePlan, Record, CheckHealth)
                        windows.go  which windows are worth offering
                        command.go  `recordo record` (and a bare `recordo` off a terminal)
                        tab.go      the record tab: picker, preview, countdown, PiP
  render/             capture + sidecars → export.mp4 (export.go, pipeline.go, command.go)
  compress/           fit an export under a size (compress.go, command.go)
  recordings/         list, prune, rename, delete (command.go, tab.go)
  settings/           config.toml (command.go, tab.go)
  doctor/             doctor, windows, devices (command.go)
internal/tui/         the full-screen app's shell: tabs, layout, status, key routing
  kit/                what tabs share: the Tab interface, drawing, popups, lists, keys
internal/cli/         what commands share: global flags, styles, loading config
internal/platform/    talks to the machine; no feature logic
  macos/              ScreenCaptureKit, event tap, AVFoundation, Accessibility, main loop
  gpu/                WebGPU compositor and shader.wgsl
  ffmpeg/             finding and probing ffmpeg/ffprobe
  clock/              mach time
internal/domain/      pure models: camera, pip, pauses, telemetry, frames, session, config
```

Three rules keep the slices independent:

- A slice imports `domain`, `platform` and the two kits (`cli`, `tui/kit`).
- A slice may call another slice's **exported use case** — `record` calls `render.Run`
  after recording, `recordings` offers render and compress — but never its internals or
  its screen.
- `platform` knows nothing about features, and `domain` knows nothing about the OS.

### Where the macOS code lives

`internal/platform/macos` is Go plus small Objective-C files, joined by cgo through one C
header, `bridge.h`. Only plain C data crosses it — numbers, structs, malloc'd strings that
Go frees — so no Objective-C object's lifetime ever leaks into Go. Windows come back as
plain `macos.Window` values (id, frame, app, title, pid); recording looks the window up
again by id. That kept every filtering and sorting rule in pure, testable Go.

## 4. The record path, step by step

Start at `record.RunCLI` (or the record tab, which calls the same `record.Record`).

**1. Load config, create a session folder.** `cli.LoadConfig` reads
`~/Library/Application Support/recordo/config.toml`, writing a commented default on first
run. `session.Create` makes `~/Movies/Recordo/2026-11-24_15-01-27/` — one folder per
recording, holding the capture, its sidecars and the finished video, so they never drift
apart. Folders are `0700`, files `0600`.

**2. Hand control to `record.Record`.** Its `RecordOptions` are functions: `Choose` (ask
which window), `OnStart`, `Stop` (block until the recording should end) and `Warn`. The
CLI and the tab supply different ones, which is why `record.go` prints nothing itself.

**3. `MakePlan` resolves what to record.** A `Target` is ask, display, app or window id.
Only windows that are *visible* are offered: `Recordable` drops system overlays, tiny
windows and windows whose centre is off every screen — macOS parks windows on another
Space, stashed by Stage Manager, or left behind by a disconnected monitor just past the
screen edge. For `-a`, the largest visible window of the app wins. The window is recorded
on the display it is actually on (`displayFor`); one that overhangs an edge gets a clear
error, because the capture is cropped from a single display.

**4. Configure the stream** (`platform/macos/recorder.m`):

- The content filter is *display-scoped with one window included*, not
  `desktopIndependentWindow`, which trips a `CGS_REQUIRE_INIT` assertion in a plain CLI
  process. Because the output is then display-sized, the window is isolated with a
  `sourceRect` crop — relative to its display — which also keeps the menu bar, dock and
  every other app out of the video.
- `pixelFormat` is pinned to **BGRA**. On recent macOS the default became 4:2:0 YUV, which
  halves colour resolution and makes text visibly soft.

**5. Capture starts.** `SCRecordingOutput` writes `capture.mp4` **directly** — video never
passes through this program, which is why capture is cheap. A second stream output logs
each frame's display time and throws the pixels away. `Record` waits for the recording
output to report it has started, so a failure surfaces now rather than as an empty file.

**6. Telemetry** (`platform/macos/telemetry.m`) runs on two C threads:

- A **poller** at 120 Hz reads the cursor position. It needs no permission, gives evenly
  spaced samples, and skips duplicates, so a gap means "stationary", never "missing".
- A **CGEventTap** for clicks, which needs Input Monitoring. It is listen-only, and its
  mask holds only mouse-down, mouse-up and scroll — **no keyboard event type, so it cannot
  observe keystrokes**. If macOS disables it mid-recording it is re-armed and counted.

If the tap cannot install, recording continues and `tap_installed: false` is recorded: the
result pans but never click-zooms.

**7. Browsers.** For a browser window, `macos.WebContentRect` asks the Accessibility API for
the exact page bounds before recording starts, so the layout matches the frames.

**8. Webcam** (`platform/macos/webcam.m`), when enabled. `AVCaptureMovieFileOutput` writes
`camera.mp4` directly, and a second output logs host-clock timestamps. Recording waits for
the file to really start before starting the screen, so a cold camera does not leave the
first second of video without an overlay, and the timestamp log is trimmed to the file's
own first frame.

**9. Stop, then write the sidecars** — `frames.json`, `telemetry.json`, `meta.json`,
`camera_frames.json` — and report `CheckHealth`.

### Pausing

ScreenCaptureKit's recording cannot be paused, so capture never stops: `domain/pauses`
logs the paused spans on the mach clock, and the render cuts them out of the screen, the
webcam and the voice over alike. `pauses.RecordedTime` maps capture time onto the timeline
with the pauses removed, which is what keeps a click zoom from expiring during a pause.

## 5. The render path, step by step

`render.Run(src, dst, opts)` in `internal/features/render/export.go`.

1. **Read the sidecars** beside the capture, so a recording is self-contained.
2. **Probe** the capture with ffprobe (`platform/ffmpeg`) for size and duration.
3. **Build a fixed 60 fps timeline.** The capture's own timing is variable —
   ScreenCaptureKit drops frames under load — so ffmpeg resamples to exactly 60 fps and
   the camera is solved on the same grid. Frame *i* is exactly *i*/60 seconds in.
4. **Move telemetry into capture pixels:** subtract the window's origin (global points),
   multiply by the Retina scale.
5. **Decide the content rectangle.** A browser with Accessibility bounds uses the exact
   page; without them, a fixed `browser_crop_top` guess; anything else, the whole frame.
   This is what removes tabs, bookmarks and URL history from the finished video.
6. **Solve the camera** (`domain/camera`) in content space.
7. **The render loop** — three stages that overlap, in `pipeline.go`:

```
ffmpeg decode ─► reader goroutine ─► GPU composite ─► writer goroutine ─► ffmpeg encode
 (rawvideo)        (3 frames ahead)    (this goroutine)   (3 frames behind)   (h264_videotoolbox)
```

The reader keeps ffmpeg's pipe draining while the GPU works, and the writer keeps the
encoder fed; the renderer itself stays on one goroutine. Overlapping the stages is why the
Go export runs about 20% faster than a strictly sequential loop. `h264_videotoolbox` is
macOS's hardware encoder.

A voice-over track is copied from `capture.mp4` as a second encoder input, with the
pauses cut by an `aselect` filter. The webcam is a third ffmpeg decode, scaled (and for a
circle, centre-cropped) so the GPU only uploads a texture at the overlay's own size;
`pip.FrameOffset` lines its first frame up with the screen's on the shared clock.

## 6. `domain/camera` — the part that decides how it *feels*

No OS or GPU dependency, so it is tuned headlessly against recorded fixtures. Two
mechanisms combine.

**The click envelope.** Each click produces a zoom weight over time: ramp in, hold, ramp
out, eased with `smoothstep`. Overlapping clicks combine with **max**, not addition, so
clicking rapidly extends one continuous zoom instead of pumping in and out — pinned by
`TestRapidClicksExtendOneZoomRatherThanPumping`.

**The cursor spring.** The camera is pulled toward the cursor by a critically damped
spring (semi-implicit Euler), so it converges without overshoot. A deadzone lets it ignore
small movements and only follow a deliberate traverse. `TestMotionIsContinuous` teleports
the cursor back and forth and asserts the camera never jumps more than 40 px in a frame —
raw tracking looks jittery and genuinely induces motion sickness.

As the zoom winds down the camera blends back to frame centre, so a recording with no
clicks sits perfectly still. Every crop is clamped inside the source.

**This is the package to experiment in.** `go test ./internal/domain/camera` runs in
milliseconds, and `zoom_percent` / `follow_hz` in the config change the feel without
re-recording.

## 7. The compositor and `shader.wgsl`

`internal/platform/gpu` drives WebGPU (wgpu-native, statically linked through
`github.com/cogentcore/webgpu`), which runs on Metal. Three things are load-bearing:

- **The Go `uniforms` struct must match the WGSL struct byte for byte.** WGSL aligns a
  `vec4` to 16 bytes, so the Go struct carries explicit padding fields.
  `TestUniformsLayoutMatchesTheShader` pins every offset and the total size.
- **`RGBA8Unorm`, not `RGBA8UnormSrgb`.** Bytes arrive from ffmpeg already sRGB-encoded; an
  implicit conversion would double-apply gamma and wash out the colours.
- **Readback rows are padded to 256 bytes**, a copy requirement, so the padding is
  stripped row by row on the way out.

The shader draws one full-screen triangle and does all the work per pixel in `fs_main`:
background (gradient or cover-fit image), the drop shadow and window box (both a
signed-distance-field rounded rectangle), the title bar with traffic lights and, for
browsers, a URL pill, the captured content sampled through the solved crop, and finally the
webcam with its own shadow and ring. The same SDF makes the webcam's circle and rounded
rectangle one code path.

**One renderer, not two.** Effects exist only in this shader; a second preview
implementation would drift from it.

## 8. The terminal interface

**Commands** use Cobra, with Charm's **Fang** for styled help, errors and `--version`.
Each slice exports `Command()` (or `Commands()`), and `cmd/recordo` adds them to the root.
Global flags (`-a`, `-w`, `-d`, `-s`, `-z`, `--mic`, `--webcam`, …) live in `internal/cli`.
The interactive `recordo config` menu is a **Huh** form.

**The full-screen app** is **Bubble Tea** with **Bubbles** and **Lip Gloss**.
`internal/tui` is a shell: it owns the tab bar, layout, status bar, jobs and the report
popup, and routes keys — to a tab that is capturing first, then popups and jobs, then
global keys, then the active tab. Each tab implements `kit.Tab` and talks back through
`kit.Host` (`SetStatus`, `ShowReport`, `StartJob`, …). Side effects — recording, the
camera, rendering, trashing — are injected, so every flow is tested with teatest against
fakes (`internal/tui/tuitest`).

The webcam preview in the record tab is drawn with Unicode **sextant** characters
(U+1FB00–1FB3B): six samples per cell, so a small thumbnail stays legible.

## 9. Two Go-specific decisions worth knowing

**The main thread belongs to macOS.** AVFoundation delivers part of a camera session's
work through the main dispatch queue, which a Go program never services by itself: without
it, the movie file never reports finishing and `stopRunning` blocks forever.
`macos.Main` locks the main goroutine to the main thread at startup, gives that thread to
macOS's run loop, and runs the program on a goroutine; camera session calls are marshalled
onto it. Any new entry point that might touch the webcam must go through `macos.Main`.

**The no-network check reads Go symbols, not socket imports.** On macOS, Go's `syscall`
package declares the libc socket functions in every binary, networking or not, so their
presence proves nothing. `scripts/check-local-only.sh` instead checks that no dialing,
listening, resolving, connection, HTTP or TLS code survived the linker, and CI fails if a
networking package enters the dependency graph.

## 10. Where to change things

| To change… | Go to |
|---|---|
| How the zoom feels — timing, tightness, follow | `internal/domain/camera`, and `[camera]` in the config |
| Look: padding, corners, shadow, gradient | `internal/platform/gpu/shader.wgsl` for behaviour, `internal/domain/config` for the knobs |
| The fake browser chrome, traffic lights, URL pill | `fs_main` in `shader.wgsl` |
| Which windows are offered | `internal/features/record/windows.go` |
| Which apps count as browsers | `IsBrowser` in `internal/domain/config` |
| Where files are written | `internal/domain/session` |
| Encoder settings, bitrate, codec | the encoder arguments in `internal/features/render/export.go` |
| A command or its flags | the slice's `command.go`; global flags in `internal/cli` |
| A tab in the full-screen app | the slice's `tab.go`; shared pieces in `internal/tui/kit` |

## 11. Running it

```sh
go run ./cmd/recordo                     # the full-screen app
go run ./cmd/recordo -a Safari -s 10     # Safari, 10 seconds
go run ./cmd/recordo render              # re-render the most recent recording
go run ./cmd/recordo doctor              # permissions and ffmpeg
go test ./...                            # everything
go run ./ci                              # what CI runs
RECORDO_TRACE=1 go run ./cmd/recordo …   # step-by-step capture trace
```

Needs `ffmpeg` (`brew install ffmpeg`), and these permissions granted to your *terminal*
in System Settings → Privacy & Security:

| Permission | Needed for | Without it |
|---|---|---|
| Screen Recording | capturing at all | hard failure (restart the terminal after granting) |
| Input Monitoring | click detection | records and pans, never click-zooms |
| Accessibility | exact browser page bounds | falls back to a fixed crop guess |
| Camera / Microphone | webcam / voice over | the recording continues without them |

See [SECURITY.md](../SECURITY.md) for what leaves the machine (nothing) and the limitations
this project does not hide.
