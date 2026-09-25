# Code tour

A guide to this codebase for someone who programs but doesn't write Rust. It explains
what each part does, why it's shaped that way, and the Rust idioms you'll meet — all
anchored to real code in this repo rather than to Rust in the abstract.

Read it alongside the source. Every named function is real; grep for it.

---

## 1. What the app does

Two phases, joined by files on disk:

```
  PHASE 1: record                      PHASE 2: render
  ───────────────                      ───────────────
  ScreenCaptureKit ──► capture.mp4  ┐
  frame timestamps ──► frames.json  ├──► camera solve ──► GPU composite ──► export.mp4
  cursor + clicks  ──► telemetry.json│
  window geometry  ──► meta.json    ┘
```

`capture.mp4` is the **raw** window recording — real tabs, no styling. `export.mp4` is the
finished video with the auto-zoom, rounded corners, shadow and background.

Phase 2 never needs phase 1 to re-run. Change a setting, re-render, done. That separation
is the main architectural decision in the project, and most of the file layout follows
from it.

## 2. The one idea that makes it work

macOS timestamps captured video frames and mouse events with **the same clock** —
`mach_absolute_time`. That's the linchpin.

ScreenCaptureKit gives no per-frame cursor information, so cursor position is captured
completely separately (`telemetry.rs`) from the video (`frames.rs`). Because both carry
mach timestamps, they can be lined up afterwards: for any frame, you can ask "where was
the cursor at that instant?" and get a real answer.

`clock.rs` is the whole of that conversion — 28 lines. `recorder::health()` exists purely
to prove the alignment held on a given recording; if that ever fails, the design is void.

## 3. Repo layout

```
Cargo.toml              workspace root — lists `cli` as its only member
cli/
  Cargo.toml            the actual package: name `recordo`
  build.rs              finds the Swift runtime on Command-Line-Tools-only machines
  src/
    lib.rs              declares the modules — this is the library
    main.rs             the CLI binary (argument parsing, subcommands)
    ui.rs               terminal output: picker, spinner, doctor, health report
    ...                 the modules below
    bin/webprobe.rs     a second, standalone diagnostic binary
```

`lib.rs` contains nothing but `pub mod camera;` and friends. That's Rust's module
declaration: it means "the file `camera.rs` is part of this library, and it's public."

**Why a library plus a binary?** `main.rs` is a separate program that *uses* the library,
which is why it writes `recordo::pick::capturable(...)` with the crate prefix while
`recorder.rs` writes `crate::pick::capturable(...)` — same function, different vantage
point. The split means the recording and rendering logic has no dependency on the terminal
UI and could be driven by a GUI later.

### Module map

| Module | Job | OS-dependent? |
|---|---|---|
| `camera.rs` | **The heart.** Telemetry → one crop rectangle per output frame. | No — pure math, unit-tested |
| `clock.rs` | mach absolute time → nanoseconds | Thin FFI |
| `recorder.rs` | Drives ScreenCaptureKit: plan, record, health-check | Yes |
| `telemetry.rs` | Cursor polling + click event tap | Yes |
| `frames.rs` | Logs each frame's display timestamp as it arrives | Yes |
| `pick.rs` | Filters the window list to ones worth offering | Yes |
| `webarea.rs` | Asks a browser via Accessibility where the web page is | Yes |
| `session.rs` | Where recordings and config live on disk | No |
| `config.rs` | The TOML settings file, and `config get`/`set` | No |
| `exporter.rs` | Render pipeline: ffmpeg → GPU → ffmpeg | No (spawns ffmpeg) |
| `render.rs` | wgpu compositor setup and per-frame draw | GPU |
| `shader.wgsl` | The actual pixel work | GPU |

---

## 4. The record path, step by step

Start at `main.rs::cmd_record`.

**1. Load config, create a session folder.**

```rust
let mut config = Config::load_or_create(session::config_path()?)?;
let session = Session::create()?;
```

`config_path()` is `~/Library/Application Support/recordo/config.toml`; if it
doesn't exist a commented default is written. `Session::create()` makes
`~/Movies/Recordo/2026-09-24_09-13-02/` — one folder per recording, holding the
capture, its three sidecars and the finished video, so they can never drift apart.

**2. Hand control to `recorder::record()`.** Note its signature:

```rust
pub fn record(
    session: &Session,
    target: &Target,
    config: &Config,
    chooser: impl FnOnce(&[SCWindow]) -> Result<Option<SCWindow>>,
    on_start: impl FnOnce(&Plan),
    stop: impl FnOnce(),
) -> Result<()>
```

The last three parameters are **functions passed as arguments**. `main.rs` supplies
`ui::choose_window` (how to ask which window), a closure that prints the banner, and
`|| ui::wait_for_stop(cli.seconds)` (how long to wait). This is why `recorder.rs` contains
no `println!` for interaction — all presentation lives in `ui.rs`.

**3. `plan()` resolves what to record.** The `Target` enum is `Ask`, `Display`,
`App(String)` or `Window(u32)`. For `App`, it collects matching windows, sorts by area and
takes the largest — apps have inspector panels and popovers you don't want.

It also refuses windows on another Space:

```rust
if f.origin.x < 0.0 || f.origin.y < 0.0
    || f.origin.x + f.size.width > dw + 1.0 { ... return Err(...) }
```

macOS reports a bogus origin for windows parked on another Space. Without this check
you'd get a silent black rectangle instead of an error.

**4. Configure the stream.** Two things worth understanding:

- The content filter is *display-scoped with one window included*, not
  `desktopIndependentWindow`. The comment explains why: the latter trips a
  `CGS_REQUIRE_INIT` assertion in a plain CLI process with no window-server connection.
- Because the filter is display-scoped, the output is display-sized, so the window is
  isolated by a `source_rect` crop instead. That's what keeps the menu bar, dock, desktop
  and every other app out of the video — a privacy property as much as a framing one.

**5. Two capture streams start.**

- `SCRecordingOutput` writes `capture.mp4` **directly** — video never passes through this
  program during recording. That's why capture is cheap.
- `FrameLogHandler` (in `frames.rs`) is attached as an output handler purely to record
  each frame's `display_time`. It throws the pixels away.

**6. `TelemetryRecorder::start()` spawns two threads** (`telemetry.rs`):

- A **poller** at 120 Hz reading cursor position via `CGEventSource`. This needs no
  permission at all, produces evenly spaced samples that are easy to smooth, and keeps
  producing them while the mouse sits still. Duplicate positions are skipped, so a gap in
  the data means "stationary", never "missing".
- A **CGEventTap** for clicks, which is the only way to see them and does need Input
  Monitoring. It's `ListenOnly`, so it can never delay or swallow your real input, and its
  event mask contains only mouse-down, mouse-up and scroll — **no keyboard event type, so
  it cannot observe keystrokes.** The runloop is pumped in 100 ms slices rather than
  blocking, so the stop flag is noticed promptly.

If the tap fails to install, recording continues and `tap_installed: false` is recorded.
The result is "pans but never click-zooms" rather than a failure.

**7. Before recording starts**, if the target is a browser, `webarea::web_content_rect()`
asks the Accessibility API for the exact page bounds. Doing this up front means the layout
matches the frames that follow.

**8. Sidecars are written**, then `health()` reports how well the two streams lined up.

## 5. The render path, step by step

`exporter::run_with(src, dst, zoom)`.

**1. Find the sidecars.** They're resolved relative to the capture's own folder, so a
recording is self-contained and the tool works from any working directory.

**2. `probe_video()`** shells out to `ffprobe` for width, height and duration.

**3. Build a fixed 60 fps timeline.**

```rust
let n_frames = (duration_s * FPS as f64).round() as u64 + 1;
let times: Vec<u64> = (0..n_frames).map(|i| base_t + i * 1_000_000_000 / FPS).collect();
```

The capture's real frame timing is variable — ScreenCaptureKit drops frames under load.
ffmpeg is asked to resample to exactly 60 fps, so frame *i* is exactly *i*/60 seconds in,
and the camera is solved on that same grid. Both sides now agree on what frame 900 means.

**4. Move telemetry into capture-pixel space.** Cursor coordinates are global screen
points; the capture may be a window somewhere on screen recorded at 2× Retina. `meta.json`
supplies the origin and scale, so: subtract origin, multiply by scale.

**5. Decide the content rectangle.** Three cases:

- Browser with accessibility bounds → use the exact page rect.
- Browser without → fall back to cropping `browser_crop_top` points off the top. Only a
  guess; it breaks when a bookmarks bar is toggled.
- Anything else → the whole frame.

This crop is what removes tabs, bookmarks, the profile avatar and URL history from the
finished video, replacing them with a drawn frame.

**6. Solve the camera** in content space, then shift the crops back into full-frame
coordinates.

**7. The render loop** — the core of the pipeline:

```
ffmpeg -i capture.mp4 -vf fps=60 -f rawvideo -pix_fmt rgba -   │ decoder, stdout
      │
      └─► read exactly w*h*4 bytes ─► Renderer::render(frame, crop) ─► out buffer
                                                                          │
ffmpeg -f rawvideo ... -c:v h264_videotoolbox ... export.mp4   │ encoder, stdin ◄┘
```

Two ffmpeg processes with this program in the middle, one frame at a time. A short final
read (`UnexpectedEof`) just means the stream ended. `h264_videotoolbox` is hardware
encoding, which is why the export beats real time.

## 6. `camera.rs` — the part that decides how it *feels*

No OS or GPU dependency, so it can be tuned headlessly against recorded fixtures. Two
mechanisms combine.

### The click envelope

Each click produces a zoom weight over time: ramp in (`zoom_in_s`), hold (`hold_s`), ramp
out (`zoom_out_s`). `smoothstep` gives ease-in-out rather than a linear ramp.

Crucially, overlapping clicks combine with **max**, not addition:

```rust
best = best.max(click_envelope(dt, cfg));
```

So clicking rapidly *extends one continuous zoom* instead of pumping in and out. There's a
test for exactly this (`rapid_clicks_extend_one_zoom_rather_than_pumping`).

### The cursor spring

The camera doesn't track the cursor — it's pulled toward it by a **critically damped
spring**, integrated with semi-implicit Euler:

```rust
vx += (-2.0 * omega * vx - omega * omega * (cx - tx)) * dt;
cx += vx * dt;
```

Critically damped means it converges without overshoot — no wobble. `follow_hz` sets the
spring frequency: higher tracks more tightly, lower feels calmer. Tracking the raw
coordinates instead looks jittery and genuinely induces motion sickness; the test
`motion_is_continuous` pins this by teleporting the cursor back and forth and asserting
the camera never jumps more than 40 px in a frame.

Two more details worth noticing:

```rust
let dt = (...).clamp(0.0, 0.1);   // a long stall can't blow up the integrator
```

```rust
let ccx = width / 2.0 + (cx - width / 2.0) * env;   // blend to centre as we zoom out
```

The second means a recording with no clicks sits perfectly still — the camera returns to
frame centre rather than drifting around at 1× zoom.

Finally every crop is clamped inside the source bounds, which the test
`crop_always_within_source_bounds` enforces with the cursor pinned to a corner.

**This is the file to experiment in.** `cargo test --lib` runs its five tests in
milliseconds, and `zoom_percent` / `follow_hz` in the config change how the output feels
without re-recording anything.

## 7. `render.rs` and `shader.wgsl`

`render.rs` is mostly wgpu boilerplate: create a device, a source texture, a target
texture, a uniform buffer, a bind group, a pipeline. Three things are actually load-bearing:

- **The `Uniforms` struct must match the WGSL struct exactly**, hence the `_pad` fields.
  GPUs have alignment rules that Rust doesn't; the padding satisfies them by hand.
- **`Rgba8Unorm`, not `Rgba8UnormSrgb`.** Bytes arrive from ffmpeg already sRGB-encoded, so
  an implicit conversion would double-apply gamma and wash out the colours.
- **Readback rows are padded to 256 bytes** — a `copy_texture_to_buffer` requirement — so
  the loop at the end strips that padding back out row by row.

The shader draws one full-screen triangle (no vertex buffer — the positions are computed
from the vertex index) and does all the work per pixel in `fs_main`, in this order:

1. Background: vertical gradient, or a cover-fit centre-cropped image.
2. Drop shadow: the same rounded box, offset and softened, via a signed distance field.
3. The window box.
4. If chrome is enabled: the title bar strip, three traffic lights, and for browsers a URL
   pill.
5. The captured content, sampled through the crop window the camera solved.

`rounded_box_sdf` is the standard signed-distance-field rounded rectangle — it returns the
distance to the shape's edge, which makes both antialiasing and the soft shadow one
`smoothstep` each.

---

## 8. Rust you'll meet in this code

### `Result` and `?`

This is Go's `if err != nil` with less ceremony. `?` means "if this failed, return the
error to my caller."

```rust
let content = SCShareableContent::get()
    .context("could not read shareable content — grant Screen Recording permission")?;
```

`.context(...)` attaches a human-readable message as the error travels up (the `anyhow`
crate). It's why failures print something useful instead of an errno.

### `Option<T>` instead of null

Either `Some(value)` or `None`, and the compiler will not let you use it without handling
both cases. All of these are null-handling:

```rust
.unwrap_or(10)              // default if None
.and_then(|v| v.parse())    // do this only if Some
.is_some_and(|a| a == b)    // true only if Some and the test passes
let Some(app) = w.owning_application() else { continue };   // early-out
```

### Ownership and borrowing

`&T` is a shared, read-only reference; `&mut T` is exclusive and writable. Only one
`&mut` may exist at a time, which is how the compiler rules out data races at compile
time. Copies are explicit — when you see `.clone()`, memory is genuinely being duplicated.

### Threads: `Arc<Mutex<Vec<T>>>`

In `telemetry.rs`, three threads append to one list. `Arc` = shared ownership by reference
count; `Mutex` = the lock. It reads as noisier than Go, and in exchange the compiler
makes it *impossible* to touch the `Vec` without holding the lock:

```rust
if let Ok(mut guard) = events.lock() {
    guard.push(TelemetryEvent { ... });
}
```

### Traits — Rust's interfaces

```rust
impl SCStreamOutputTrait for FrameLogHandler {
    fn did_output_sample_buffer(&self, sample: CMSampleBuffer, of_type: SCStreamOutputType)
```

"`FrameLogHandler` can act as a stream output." That's the callback macOS invokes for
every captured frame.

### Derive macros

```rust
#[derive(Debug, Clone, Copy, Serialize, Deserialize)]
pub struct TelemetryEvent { ... }
```

Code generated at compile time. `Serialize`/`Deserialize` is the entire reason
`telemetry.json` works with no marshalling code written by hand.

### Pattern matching

`match` must cover every case, and it destructures as it goes. This tuple match in
`exporter.rs` picks the content rectangle in one expression:

```rust
match (web, chrome_mode) {
    (Some(r), Chrome::Browser) => { /* exact page bounds */ }
    (_,       Chrome::Browser) => { /* fixed-crop fallback */ }
    _                          => { /* whole frame */ }
}
```

### Iterator chains

```rust
let clicks: Vec<u64> = tel.events.iter()
    .filter(|e| e.kind == EventKind::Down)
    .map(|e| e.t_ns)
    .collect();
```

Lazy until `.collect()`, and compiles down to roughly the loop you'd have written.

### `unsafe`

It does **not** mean "dangerous". It means "the compiler's guarantees stop here; I have
checked this by hand." In this codebase it appears only around calls into C and
Objective-C APIs, confined to three files: `clock.rs`, `telemetry.rs` and `webarea.rs`.
Everything else is machine-verified. That property is exactly what you'd be giving up by
rewriting the FFI layer in another language.

### Small syntax notes

- `pub` = exported, like a capital initial in Go.
- `&str` is a borrowed string view; `String` owns its bytes. Same for `&Path`/`PathBuf`.
- `let mut x` — variables are immutable unless you say otherwise.
- `impl Foo { ... }` — methods live in a block separate from the struct definition.
- `.with_width(w).with_height(h)` — builder chains; each call consumes and returns the
  config.
- `#[cfg(target_os = "macos")]` — conditional compilation. `#[cfg(test)]` marks the test
  module at the bottom of a file, which is where Rust conventionally keeps unit tests.
- `//!` at the top of a file documents the module; `///` documents the item below it.

---

## 9. Where to change things

| To change… | Go to |
|---|---|
| How the zoom feels — timing, tightness, follow | `camera.rs`, and `[camera]` in the config |
| Look: padding, corners, shadow, gradient | `shader.wgsl` for behaviour, `config.rs` for the knobs |
| The fake browser chrome, traffic lights, URL pill | `fs_main` in `shader.wgsl` |
| Which windows appear in the picker | `pick.rs` |
| Which apps count as browsers | `is_browser()` in `config.rs` |
| Where files are written | `session.rs` |
| Encoder settings, bitrate, codec | the `encoder` command in `exporter.rs` |
| CLI flags and subcommands | `main.rs` |
| Terminal output and prompts | `ui.rs` |

## 10. Running it

```sh
cargo run                      # interactive: pick a window, Enter to stop
cargo run -- -a Safari -s 10   # Safari, 10 seconds
cargo run -- render            # re-render the most recent recording
cargo run -- doctor            # check permissions and that ffmpeg is present
cargo run -- windows           # list recordable windows and their ids
cargo run -- config show       # every setting and its current value
cargo test --lib               # the camera model's unit tests
```

Needs `ffmpeg` on PATH (`brew install ffmpeg`), and three macOS permissions granted to
your *terminal*, in System Settings → Privacy & Security:

| Permission | Needed for | Without it |
|---|---|---|
| Screen Recording | capturing at all | hard failure (requires restarting the terminal after granting) |
| Input Monitoring | click detection | records and pans, never click-zooms |
| Accessibility | exact browser page bounds | falls back to a fixed crop guess |

---

## 11. Caveats about this document

Written 2026-09-24, while the CLI layer was being actively refactored — `main.rs`,
`ui.rs`, `exporter.rs`, `session.rs` and `config.rs` were all in flux. Function names and
structure should hold; exact line contents may have moved. `camera.rs`, `clock.rs`,
`frames.rs`, `pick.rs` and `webarea.rs` were stable.

See [SECURITY.md](SECURITY.md) for what leaves the machine
(nothing) and the hardening work outstanding.
