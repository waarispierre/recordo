# Proposal: keystroke overlay

**Status: proposed, not planned.** Nothing here is implemented, and no work is scheduled.

Show the keys pressed during a recording, drawn onto the exported video. Primarily for
terminal demos, where the commands being typed *are* the content.

```toml
[capture]
# Show the keys you press in the exported video. Off by default — see the warning below.
keystrokes = false
```

One switch would drive both halves: it puts `KeyDown` into the event tap at record time,
and draws the overlay at render time.

---

## Read this before implementing

recordo currently **cannot observe a keystroke**. The event tap registers mouse and scroll
events only, so keyboard input is invisible to it even when Input Monitoring is granted.
[SECURITY.md](../../SECURITY.md) states that as a guarantee.

This feature is the single change that would remove it. Three things make that sharper
than it first appears:

- **No new OS permission is involved.** Input Monitoring is already granted for click
  detection, and macOS does not distinguish mouse taps from keyboard taps within it.
  Flipping this boolean would be the *only* gate — the system will not prompt again.
- **Everything typed is captured, not just what you meant to demo.** A password typed into
  another app mid-recording would land in `telemetry.json` and be burned into
  `export.mp4`.
- **It persists.** Turning the setting back off later would not remove keystrokes already
  written into past sidecars.

One mitigation comes free: while macOS **secure input** is active — any focused password
field, `sudo` at a terminal prompt, Keychain — event taps receive nothing at all. The
highest-risk keystrokes are therefore already invisible to this tool, by the OS. That is
real protection and worth documenting, but it is not complete: a password typed into a web
form that does not trigger secure input **would** be captured.

If this is built, SECURITY.md must be updated in the same change. A guarantee that
quietly becomes conditional is worse than one that was never made.

### Safeguards this feature must ship with

| | Safeguard | Status |
|---|---|---|
| a | Default `false`. Opt-in only, never inferred. | — |
| b | A visible warning at the start of **every** recording where it is enabled, not only when the setting changes. | — |
| c | `0600`/`0700` file permissions, since sidecars would then hold typed text. | **already done** |
| d | `doctor` reports whether keystroke capture is on. | — |
| e | README documents the secure-input behaviour, so the protection is understood rather than assumed. | — |
| f | A `recordo scrub <session>` command that strips `key` fields from a sidecar, for when you realise afterwards. | — |

---

## Design

Three separable pieces, in the grain of the existing architecture — OS access, pure
model, rendering.

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
| 1 | `src/config.rs` | New `[capture]` section with `keystrokes: bool` (default `false`); entry in `DEFAULT_TOML` carrying the warning. |
| 2 | `src/capture/keycode.rs` *(new)* | Virtual keycode + modifier flags → label. Pure, tested. |
| 3 | `src/capture/telemetry.rs` | `EventKind::Key`, `key: Option<String>` with `#[serde(default)]`, conditional tap mask, `start(capture_keys)`. |
| 4 | `src/capture/recorder.rs` | Pass `config.capture.keystrokes` through; record it in `meta.json` so render knows whether keys exist. First real use of the `config` parameter it currently discards (`let _ = config;`). |
| 5 | `src/keys.rs` *(new)* | Grouping, fade and truncation model. Pure, tested. |
| 6 | `src/render/overlay.rs` *(new)* | CoreText label rasterization with a cache. |
| 7 | `src/render/mod.rs` | Blend the overlay during the existing readback row walk. |
| 8 | `src/render/export.rs` | Build the overlay track beside the crops; add a line to `Report`. |
| 9 | `src/app/ui.rs` | Warning at record start (safeguard b); `doctor` line (d); key count in `health_report`. |
| 10 | `src/main.rs` | `scrub` subcommand (safeguard f). |
| 11 | README, `docs/code-tour.md` | Document the setting, the secure-input behaviour, and the new modules. |

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
