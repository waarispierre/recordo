# Security review & hardening plan

Reviewed 2026-09-23 against the working tree at that time. The goal set for the app:
**everything stays on this machine — no recording, telemetry or metadata ever leaves it.**

Note: the tree was being actively edited during the review (crate renamed `spike` →
`recordo`, `recorder.rs` and `ui.rs` added, `main.rs` / `exporter.rs` / `config.rs` /
`session.rs` all rewritten between 23:44 and 23:53). Line numbers below are from that
snapshot; function names are the reliable anchor.

> **Status 2026-09-24: Parts 2 and 3 implemented.** See "Part 6 — Implementation record"
> at the end, which also corrects one claim in Part 1 that did not survive verification.

---

## Part 1 — What was verified

The "completely local" property holds in the current code. Four independent checks:

1. **No network code in the dependency graph.** All 200 crates in `Cargo.lock` were
   enumerated. No `reqwest`, `hyper`, `tokio`, `ureq`, `curl`, `rustls`, `native-tls`,
   `openssl`, `socket2`. No analytics, telemetry or crash-reporting crate.

2. **The compiled binary cannot open a socket.**
   - `otool -L` links only ApplicationServices, CoreGraphics, CoreFoundation,
     QuartzCore, Metal, Foundation, ScreenCaptureKit, CoreMedia, IOSurface, MetalFX,
     CoreVideo, AVFoundation, AppKit, ImageIO, VideoToolbox, libSystem, libobjc,
     libiconv, libc++ and the Swift runtime. **No CFNetwork, no Network.framework, no
     Security.framework.**
   - `nm -u` imports no `_socket`, `_connect`, `_bind`, `_sendto`, `_getaddrinfo`, and no
     `NSURLSession`. The single stream symbol present is `_CFStreamCreateBoundPair`,
     which is an in-memory pipe, not a socket.

3. **The event tap is mouse-only.** `telemetry.rs::spawn_tap` registers exactly
   `LeftMouseDown/Up`, `RightMouseDown/Up`, `OtherMouseDown/Up` and `ScrollWheel`, in
   `ListenOnly` mode. No keyboard event type is in the mask, so the tap cannot observe
   keystrokes even with Input Monitoring granted. Cursor position is polled via
   `CGEventSource`, which needs no permission at all.

   > **This property is deliberately changed by Part 5.** The keystroke-overlay feature
   > adds `KeyDown` to the mask when the user opts in. Everything in Part 5 is written
   > around keeping that opt-in explicit, visible and default-off.

4. **Dependency provenance is clean.** The unfamiliar-looking `doom-fish-utils`,
   `apple-cf` and `apple-metal` are the `screencapturekit-rs` author's own crates
   (Per Johansson, github.com/doom-fish), pulled in transitively by `screencapturekit`.
   No network calls in their sources. No dependency build script performs network access.

**Conclusion:** no Rust code in this project can reach the network. Every residual risk is
in the subprocesses it spawns (`ffmpeg`, `ffprobe`, `open`, `date`, `which`, `$EDITOR`) or
in on-disk permissions.

---

## Part 2 — Findings

### 1. ffmpeg is not pinned to the file protocol — Medium

`exporter.rs::run_with` spawns `ffmpeg -i <src>` and `exporter.rs::probe_video` runs
`ffprobe <path>` with no protocol restriction. `recordo render <path>` accepts an
arbitrary path. ffmpeg's `hls`, `concat` and `dash` demuxers follow references *contained
inside* an input file, including remote URLs.

This is the only realistic path by which data from this machine could travel over the
wire, and equally a path by which remote content could be pulled in.

### 2. Every helper binary is resolved through `$PATH` — Medium

`Command::new("ffmpeg")`, `Command::new("ffprobe")` (`exporter.rs`),
`Command::new("open")` (`main.rs::cmd_record`, `main.rs::cmd_render`),
`Command::new("date")` (`session.rs::local_utc_offset_secs`) and
`Command::new("which")` (`ui.rs::which`).

This process holds Screen Recording, Accessibility and Input Monitoring grants. A binary
planted in an earlier `$PATH` entry inherits all three — the TCC grants are the asset
worth stealing here, not the code.

`$EDITOR` in `main.rs::cmd_config` is also exec'd, but that is conventional CLI behaviour
and explicitly user-chosen; no change proposed.

### 3. Recordings are world-readable — Medium

`session.rs::recordings_dir` and `Session::create` use `create_dir_all`, giving 0755.
Sidecars go out through `std::fs::write` at 0644. On a shared Mac, every other local
account can read your screen recordings and the cursor-track sidecars.

### 4. No `.gitignore` — Low/Medium

`out/capture.mp4` — a real recording — currently sits in the project directory, alongside
`telemetry.json` and `meta.json`. The directory is not yet a git repo; the first
`git init && git add . && git push` sends the recording off the machine. This is the most
likely way the local-only property actually gets broken in practice.

### 5. Accessibility values cast to `CFArray` without a type check — Low

`webarea.rs::children_of`, `::ax_windows` and `::web_content_rect` each do
`CFArray::wrap_under_get_rule(value.as_CFTypeRef() as CFArrayRef)` on whatever the
Accessibility API returned for `AXChildren` / `AXWindows`. A target app returning a
non-array aborts the process through CoreFoundation's type validation. Crash only — not
memory corruption — but it is reachable from any third-party app you point the tool at.

`frame_of` is safe: `AXValueGetValue` validates the type itself and returns false.

### 6. ffmpeg argument injection on the output path — Low

ffmpeg has no `--` terminator, so a `dst` beginning with `-` is parsed as a flag rather
than a filename. Only reachable through the user's own argv, so low impact, but the guard
is one line.

### 7. Sidecar paths were hardcoded to `out/` — not a security issue, now fixed

At review time `exporter.rs::run_with` read `out/telemetry.json`, `out/frames.json` and
`out/meta.json` literally, while recordings had moved to
`~/Movies/DanielRecordo/<timestamp>/`. Resolved in the refactor on 2026-09-24: sidecars
are now derived from the capture's own parent directory. No action needed.

---

## Part 3 — Plan

| # | File | Change |
|---|---|---|
| 1 | `cli/src/tools.rs` *(new)* | Resolve `ffmpeg`/`ffprobe` to an absolute path, searching `/opt/homebrew/bin`, `/usr/local/bin`, `/opt/local/bin`, `/usr/bin`, `/bin` before falling back to `$PATH`. Plus `check_not_option_like()` for finding 6. Register in `lib.rs`. |
| 2 | `cli/src/exporter.rs` | Spawn through the resolved absolute paths. Add `-nostdin -protocol_whitelist file,pipe` to the decoder and to `ffprobe` — **to those two only, never the encoder**, whose input is our own stdin pipe. See Part 6.1: applying it to the encoder breaks rendering completely. |
| 3 | `cli/src/session.rs` | `0700` on `~/Movies/DanielRecordo`, on each session folder and on the config dir. Add `write_private()` (0600) for sidecars. Use `/bin/date` for the timezone offset. |
| 4 | `cli/src/recorder.rs` | Write `telemetry.json`, `frames.json`, `meta.json` through `write_private()`. |
| 5 | `cli/src/main.rs`, `cli/src/ui.rs` | `/usr/bin/open` instead of `open`. Replace `ui::which` with `tools::find` so `doctor` reports the absolute binary that will actually be executed. |
| 6 | `cli/src/webarea.rs` | `CFGetTypeID(value) == CFArrayGetTypeID()` check before each of the three `CFArray` casts; return empty/None on mismatch. |
| 7 | `.gitignore` *(new)* | `target/`, `out/`, `recordo.toml`. |
| 8 | `scripts/check-local-only.sh` *(new)* + README | Assert the built binary links no network framework and imports no socket symbol, so the guarantee is enforced on every build rather than assumed. Short README section on what is written where. |

Items 2, 5 touch files that were being rewritten during the review and may need rebasing.

---

## Part 4 — Residual notes, no code change proposed

- **`capture.mp4` keeps the unredacted window.** Browser chrome — tabs, URL bar, bookmarks
  — is cropped only in `export.mp4`. The raw capture is retained deliberately so
  re-exports need no re-recording, but it means the unredacted version stays on disk
  indefinitely. Worth a documented `recordo prune`, or a note in the README.
- **`telemetry.json` holds global cursor coordinates** for the whole session, including
  movement outside the recorded window. Minor, stays local, but it is more than the
  recorded region.
- **`meta.json` records `bundle_id` and `app_name`** of the recorded app. Not window
  titles. Reasonable.
- **ffmpeg itself is a large untrusted-input parser** processing your own capture. Pinning
  the protocol (item 2) is the meaningful mitigation; sandboxing it further is out of
  proportion for locally-produced input.

---

## Part 5 — Feature: keystroke overlay

Requested 2026-09-24. A boolean config setting that shows the keys pressed during a
recording, drawn onto the exported video. Primarily for terminal demos, where the commands
being typed are the content.

```toml
[capture]
# Show the keys you press in the exported video. Off by default — see the warning below.
keystrokes = false
```

One switch drives both halves: it puts `KeyDown` into the event tap at record time, and
draws the overlay at render time. `config set capture.keystrokes true` works through the
existing `flatten`/`set_value` machinery with no extra code.

### Why this one needs care

Today this app cannot see a keystroke. This feature is the single change that makes it
able to, and three things make that sharper than it first appears:

- **No new OS permission is involved.** Input Monitoring is already granted for click
  detection, and macOS does not distinguish mouse taps from keyboard taps within it.
  Flipping this boolean is therefore the *only* gate — the system will not prompt again.
- **Everything typed is captured, not just what you meant to demo.** A password typed into
  an app mid-recording lands in `telemetry.json` and is burned into `export.mp4`.
- **It persists.** Turning the setting back off later does not remove keystrokes already
  written into past sidecars.

One mitigation comes free: while macOS **secure input** is active — any focused password
field, `sudo` at a terminal prompt, Keychain — event taps receive nothing at all. So the
highest-risk keystrokes are already invisible to this tool, by the OS. That is a real
protection and worth stating in the README, but it is not complete: a password typed into
a web form that doesn't trigger secure input *will* be captured.

**Safeguards this feature must ship with:**

| | Safeguard |
|---|---|
| a | Default `false`. Opt-in only, never inferred. |
| b | A visible warning line at the start of every recording where it's enabled — not just at the moment the setting is changed. |
| c | Plan item 3 (0600/0700 file permissions) becomes a **prerequisite**, not a nice-to-have. Sidecars now hold typed text. |
| d | `doctor` reports whether keystroke capture is on. |
| e | README documents the secure-input behaviour, so the protection is understood rather than assumed. |
| f | A `recordo scrub <session>` command that strips `key` fields from a sidecar, for when you realise afterwards. |

### Design

Three separable pieces, in the grain of the existing architecture — OS access, pure model,
rendering.

**1. Capture — `telemetry.rs` + new `keycode.rs`**

- Add `EventKind::Key`; add `key: Option<String>` to `TelemetryEvent`, marked
  `#[serde(default)]` so existing sidecars without the field still parse.
- `TelemetryRecorder::start(capture_keys: bool)`. When true, add `CGEventType::KeyDown` and
  `FlagsChanged` to the tap's event mask. When false the mask is exactly what it is today,
  so the current behaviour is bit-for-bit unchanged.
- `keycode.rs` maps virtual keycode + modifier flags → a display label: `⌘C`, `⏎`, `⇥`,
  `⎋`, `⌃C`, or the literal character. Pure lookup, unit-testable, no OS calls.

  Deliberately decoding from the **keycode**, not from `CGEventKeyboardGetUnicodeString`.
  Same result for normal typing, but it keeps the door open to a chords-only mode later
  without restructuring.

**2. Model — new `keys.rs`**

A pure function in the style of `camera.rs`: telemetry + frame timestamps → one
`Option<KeyOverlay { text, alpha }>` per frame.

- Consecutive keys within ~1.2 s group into a single line, so `ls -la⏎` reads as one
  command rather than six flashes.
- The line fades out after ~1.5 s idle, using the same `smoothstep` easing the click
  envelope uses.
- Long lines truncate from the left, keeping the most recent keys visible.

No OS or GPU dependency, so it gets unit tests like the camera model does.

**3. Render — new `overlay.rs`**

Rasterize each distinct label once to an RGBA pill via CoreText (the `core-text` crate),
cache it in a `HashMap<String, Bitmap>`, and alpha-blend it into the frame.

**Blend on the CPU, in the readback buffer** — `render.rs::render` already walks the output
row by row to strip the 256-byte padding, so the pill can be composited in the same pass.
A ~400×80 region at 60 fps is ~32k pixels a frame, which is nothing.

The alternative — doing it in the shader — means a new bind group, a new texture and a
changed `Uniforms` layout, and that struct's hand-written padding has to stay byte-exact
with the WGSL. Not worth it for a static overlay. Recommend CPU.

### Work items

| # | File | Change |
|---|---|---|
| 1 | `config.rs` | New `[capture]` section with `keystrokes: bool` (default `false`); entry in `DEFAULT_TOML` carrying the warning. |
| 2 | `keycode.rs` *(new)* | Virtual keycode + modifier flags → label. Pure, tested. |
| 3 | `telemetry.rs` | `EventKind::Key`, `key: Option<String>` with `#[serde(default)]`, conditional tap mask, `start(capture_keys)`. |
| 4 | `recorder.rs` | Pass `config.capture.keystrokes` through; record it in `meta.json` so render knows whether keys exist. First real use of the `config` parameter it currently discards (`let _ = config;`). |
| 5 | `keys.rs` *(new)* | Grouping, fade and truncation model. Pure, tested. |
| 6 | `overlay.rs` *(new)* | CoreText label rasterization with a cache. |
| 7 | `render.rs` | Blend the overlay during the existing readback row walk. |
| 8 | `exporter.rs` | Build the overlay track beside the crops; add a line to `Report`. |
| 9 | `ui.rs` | Warning at record start (safeguard b); `doctor` line (d); key count in `health_report`. |
| 10 | `main.rs` | `scrub` subcommand (safeguard f). |
| 11 | README, `CODE-TOUR.md` | Document the setting, the secure-input behaviour, and the new modules. |

Items 1–5 and 9 are straightforward. **Item 6 is the only real unknown** — check
`core-text` compatibility against the pinned `core-graphics 0.25` before committing to it.
If it fights, the fallback is a pre-baked bitmap font atlas for the ~100 key labels, which
is more tedious but has no dependency risk.

### Two decisions for you

1. **Overlay position.** Bottom-centre, floating over the content, is the convention
   (KeyCastr, Screen Studio). The alternative is a strip in the padding area below the
   window, which never occludes content but pushes the output taller. Recommend
   bottom-centre with a configurable inset.
2. **Full text or chords only.** Full text is required for the terminal use case you
   described, so that's the default I've planned for. A later `keystrokes = "chords"` mode
   — showing only `⌘`/`⌃`/`⌥` combinations and special keys, never plain characters —
   would be the safer setting for recording anything involving a login. The `keycode.rs`
   design above keeps that cheap to add.

---

## Part 6 — Config and render fixes

### 6.1 Rendering is currently broken — fix first

**Status: live regression. `recordo render` fails on every invocation.**

Verified 2026-09-24 by running it against the most recent recording:

```
[fd @ 0x…] Protocol 'fd' not on whitelist 'file,pipe'!
[in#0 @ 0x…] Error opening input: Invalid argument
Error opening input file -.
✗ write frame to encoder: Broken pipe (os error 32)
```

Part 3 item 2 was applied to **all three** ffmpeg invocations, including the encoder. The
encoder reads frames from this program's stdin as `-i -`, and ffmpeg opens `-` through its
`fd` protocol, which `file,pipe` excludes. The encoder therefore dies before the first
frame arrives, and the render loop gets a broken pipe. `-nostdin` on the encoder is
self-contradictory for the same reason: its input *is* stdin.

Reduced to a one-line reproduction, confirming both the cause and the fix:

```sh
# fails: Protocol 'fd' not on whitelist
head -c 16 /dev/zero | ffmpeg -v error -protocol_whitelist file,pipe \
    -f rawvideo -pix_fmt rgba -s 2x2 -r 1 -i - -f null -

# succeeds
head -c 16 /dev/zero | ffmpeg -v error -protocol_whitelist file,pipe,fd \
    -f rawvideo -pix_fmt rgba -s 2x2 -r 1 -i - -f null -
```

**Fix:** remove both `-nostdin` and `-protocol_whitelist` from the encoder command in
`exporter.rs`. Its input is a pipe this program owns and its output is a path this program
chose, so neither flag buys anything there. Keep both on the decoder and `ffprobe`, where
the input is a file that could name a remote URL internally — that is where the protection
was actually aimed.

Adding `fd` to the whitelist would also work, but removing the flags is better: it keeps
the whitelist meaning exactly "the untrusted input is restricted to local files."

*My original wording — "to the decoder and to `ffprobe`" — was not emphatic enough about
excluding the encoder. Part 3 item 2 has been amended.*

### 6.2 `background_image` cannot be set from the CLI

The setting is real and fully wired — `config.rs:46`, consumed at `exporter.rs:165`, with a
cover-fit centre-crop path in the shader. But `DEFAULT_TOML` ships it **commented out**:

```toml
# background_image = "/Users/you/Pictures/backdrop.jpg"
```

Everything in the config CLI reads the *file*, not the struct, so a commented key is
invisible three ways over:

- `flatten()` walks keys present in the TOML → absent from `config show` and `config get`.
- `settings()` does the same → absent from the interactive menu.
- `set_value()` has `if !table.contains_key(field) { bail!("no such setting") }` →
  `config set style.background_image ~/pic.jpg` is rejected outright.

The only way to use the feature today is `config edit` and uncommenting the line by hand.

**Fix, three parts:**

1. Ship the key uncommented with an empty default, `background_image = ""`, so it appears
   in `show`, `get` and the menu.
2. Treat empty as "no image". The field stays `Option<PathBuf>` — `= ""` deserializes to
   `Some("")` — so add a `Config::background_image() -> Option<&Path>` helper that filters
   empties and expands a leading `~/` via `dirs::home_dir()`, and call it from
   `exporter.rs`. Two wins: `config set style.background_image ""` clears it again, and
   `~/Pictures/x.jpg` stops reaching `image::open` literally and failing.
3. Drop the `contains_key` guard in `set_value`, validating instead by deserializing the
   modified document into `Config` **before** writing. `deny_unknown_fields` then rejects
   genuine typos, and this is strictly better than the current write-then-check in
   `main.rs`, which leaves a bad value on disk when validation fails.

Item 3 matters beyond this one setting: it fixes the same problem for any config written
before a new setting was added, and for every optional setting added later.

### 6.3 `render` must re-render the latest recording with the current config

**Requirement:** changing a setting and running `recordo render` re-renders the most
recent recording with the new value, without re-recording.

**Status: already true by design, and worth keeping that way.** `cmd_render` with no path
calls `Session::latest()`, and `exporter::run_with` calls
`Config::load_or_create(session::config_path()?)` on every invocation — the same file
`config set` and `config edit` write to. Nothing is cached between runs.

It cannot be demonstrated end-to-end until 6.1 is fixed. Four things should be tidied so it
also *looks* reliable:

| | Cleanup |
|---|---|
| a | `config.rs:12` still declares `pub const DEFAULT_PATH: &str = "recordo.toml"`, now dead. Its module doc still says settings are "loaded from `recordo.toml` next to the project", which is no longer where they live. Both will mislead anyone editing a stray `recordo.toml` in the project directory and wondering why nothing changes. Delete the constant, correct the doc comment. |
| b | `~/Movies/Recordo` and `~/Movies/DanielRecordo` both exist after the rename. The old one is empty here, so nothing was lost — but `Session::latest()` only looks in the new one. Remove the empty legacy folder, or migrate it. |
| c | Aborted recordings leave empty session folders (`2026-09-24_00-17-42` and `2026-09-24_00-04-07` have no files). `latest()` and `list` both filter on `capture.mp4`, so they're harmless, but they accumulate. Remove the folder when recording fails before writing a capture. |
| d | `render` should print which settings actually changed the output, or at minimum the resolved zoom and padding, so "did it pick up my change?" is answerable from the output. The `Report` struct is already the place for it. |

**Suggested order:** 6.1 first — it blocks everything, including verifying 6.3 — then 6.2,
then the 6.3 cleanups.


---

## Part 5 — *(referenced by Part 1, never written)*

Part 1 note 3 defers to "Part 5" for a keystroke-overlay feature that would add `KeyDown`
to the event mask. **No such feature exists and none is planned.** The tap is mouse-only.
If a keystroke overlay is ever built, the opt-in design described there still applies:
explicit, visible, default-off.

---

## Part 6 — Implementation record (2026-09-24)

### Correction to Part 1, claim 2

Part 1 asserted the compiled binary "cannot open a socket" and that the only stream symbol
present was `_CFStreamCreateBoundPair`. **That is wrong for the current build.**

`nm -u target/release/recordo` imports five `CFSocket*` symbols, and the binary
retains `_cf_socket_create_udp_ipv4` — a *defined* Swift function. The source is
`apple-cf`, a transitive dependency of `screencapturekit`, which ships a Rust `CFSocket`
wrapper (`apple_cf::cf::runtime::CFSocket`, with a `udp_ipv4` constructor) plus a Swift
`CoreFoundationBridge` counterpart.

What is actually true, and is what `scripts/check-local-only.sh` now asserts:

- No networking framework is linked (no CFNetwork, Network.framework, Security.framework).
- No raw BSD socket syscall is imported (`socket`, `connect`, `bind`, `getaddrinfo`, …).
- No URL-loading symbol is imported (`NSURLSession`, `CFURLConnection`, …).
- No networking or TLS crate appears in `Cargo.lock`.
- Socket symbols are confined to `apple-cf`, and **no code in `cli/src` references them.**

So the capability is present but dormant. "This app never opens a socket" is accurate;
"this binary is incapable of opening one" is not, and should not be claimed.

### A bug in the verification script itself

The first version of `check-local-only.sh` used `nm -u "$BIN" | grep -q PATTERN` under
`set -o pipefail`. `grep -q` exits at the first match and closes the pipe, `nm` dies of
SIGPIPE, the pipeline reports failure, and the enclosing `if` reads as *no match*. It
printed `PASS` on a binary whose socket symbols are trivially demonstrable with
`nm -u … | grep -i socket`.

Every check in that version was silently inverted. The script now captures output into a
variable before matching. A security check that cannot fail is worse than no check.

### Findings resolved

| # | Finding | Resolution |
|---|---|---|
| 1 | ffmpeg not protocol-restricted | `-protocol_whitelist file,pipe,fd` on decoder, encoder and `ffprobe`; `-nostdin` on the decoder. Verified: `http://` input is now refused with *Protocol 'http' not on whitelist*, where stock ffmpeg attempts DNS resolution. |
| 2 | Helpers resolved through `$PATH` | New `cli/src/tools.rs` resolves `ffmpeg`/`ffprobe` against `/opt/homebrew/bin`, `/usr/local/bin`, `/opt/local/bin`, `/usr/bin`, `/bin` before `$PATH`. `/usr/bin/open` and `/bin/date` hardcoded. `doctor` prints the absolute binary it would run. |
| 3 | Recordings world-readable | `session.rs` creates directories `0700` and writes sidecars `0600` via `write_private()`. `restrict()` tightens files produced by ScreenCaptureKit and ffmpeg, which use the default umask. Verified on disk. |
| 4 | No `.gitignore` | Added: `target/`, `out/`, `*.mp4`, the three sidecars, `recordo.toml`. |
| 5 | Unchecked `CFArray` casts | `webarea.rs::as_array()` checks `CFGetTypeID == CFArrayGetTypeID` before every cast; all three sites go through it. A hostile app returning a non-array now yields an empty result instead of aborting the process. |
| 6 | ffmpeg argument injection | `tools::check_not_option_like()` rejects `src`/`dst` beginning with `-`. Unit-tested. |
| 7 | Hardcoded `out/` sidecar paths | Already fixed during the CLI refactor; sidecars derive from the capture's parent. |

### Part 4 residual addressed

`recordo prune` deletes raw captures while keeping rendered videos. The raw capture
is the **unredacted** one — for a browser it still holds the tab strip, address bar and
bookmarks that the render removes — so retaining it indefinitely meant the redaction only
ever applied to the copy you shared. `--older-than N` limits by age, `--all` removes
recordings entirely. Without `--all` it only touches recordings that already have a
render, so it cannot destroy the sole copy of anything. Confirmation is required unless
`--yes`.

### Still open

- `apple-cf`'s dormant socket wrapper cannot be removed without forking `screencapturekit`.
  Worth an upstream issue; meanwhile the check script pins it in place.
- ffmpeg remains a large parser handling your own capture. Protocol pinning is the
  proportionate mitigation; sandboxing locally-produced input is not.
- `telemetry.json` still records global cursor coordinates, including movement outside the
  recorded window. Local-only and now `0600`, but it is more than the recorded region.
