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

Three separable pieces, in the grain of the existing architecture — OS access, pure model,
rendering — all inside the record and render slices.

**1. Capture — `platform/macos/telemetry.m` + new `domain/keys/keycode.go`**

- Add `telemetry.Key` to `EventKind`; add `Key string` to `telemetry.Event` with
  `json:"key,omitempty"`, so existing sidecars without the field still parse.
- `macos.StartTelemetry(captureKeys bool)`. When true, add `kCGEventKeyDown` and
  `kCGEventFlagsChanged` to the tap's event mask. When false the mask is exactly what it
  is today, so the current behaviour is bit-for-bit unchanged.
- `keycode.go` maps virtual keycode + modifier flags → a display label: `⌘C`, `⏎`, `⇥`,
  `⎋`, `⌃C`, or the literal character. Pure lookup, unit-testable, no OS calls.

  Deliberately decoding from the **keycode**, not from `CGEventKeyboardGetUnicodeString`.
  Same result for normal typing, but it keeps the door open to a chords-only mode later
  without restructuring.

**2. Model — new `domain/keys`**

A pure function in the style of `domain/camera`: telemetry + frame timestamps → one
`*Overlay{Text string; Alpha float64}` per frame.

- Consecutive keys within ~1.2 s group into a single line, so `ls -la⏎` reads as one
  command rather than six flashes.
- The line fades out after ~1.5 s idle, using the same `smoothstep` easing the click
  envelope uses.
- Long lines truncate from the left, keeping the most recent keys visible.

No OS or GPU dependency, so it gets unit tests like the camera model does.

**3. Render — new `platform/gpu/overlay.go`**

Rasterize each distinct label once to an RGBA pill — CoreText through a small
Objective-C helper in `platform/macos`, or `golang.org/x/image/font` with an embedded
font — cache it in a `map[string]*image.RGBA`, and alpha-blend it into the frame.

**Blend on the CPU, in the readback buffer** — `gpu.Renderer.Render` already walks the
output row by row to strip the 256-byte padding, so the pill can be composited in the same
pass. A ~400×80 region at 60 fps is ~32k pixels a frame, which is nothing.

The alternative — doing it in the shader — means a new bind group, a new texture and a
changed `uniforms` layout, and that struct has to stay byte-exact with the WGSL (a test
pins it). Not worth it for a static overlay. Recommend CPU.

### Work items

| # | File | Change |
|---|---|---|
| 1 | `domain/config` | New `[capture]` section with `keystrokes` (default `false`); entry in the default file carrying the warning. |
| 2 | `domain/keys/keycode.go` *(new)* | Virtual keycode + modifier flags → label. Pure, tested. |
| 3 | `domain/telemetry`, `platform/macos/telemetry.m`, `screen.go` | `Key` event kind and field, conditional tap mask, `StartTelemetry(captureKeys)`. |
| 4 | `features/record/record.go` | Pass `cfg.Capture.Keystrokes` through; record it in `meta.json` so render knows whether keys exist. |
| 5 | `domain/keys` *(new)* | Grouping, fade and truncation model. Pure, tested. |
| 6 | `platform/gpu/overlay.go` *(new)* | Label rasterization with a cache. |
| 7 | `platform/gpu/renderer.go` | Blend the overlay during the existing readback row walk. |
| 8 | `features/render/export.go` | Build the overlay track beside the crops; add a line to `Report`. |
| 9 | `features/record` (command and tab), `features/doctor` | Warning at record start (safeguard b); `doctor` line (d); key count in the health report. |
| 10 | `features/recordings` | `scrub` subcommand (safeguard f). |
| 11 | README, `docs/code-tour.md` | Document the setting, the secure-input behaviour, and the new packages. |

Items 1–5 and 9 are straightforward. **Item 6 is the only real unknown** — choose between
CoreText (best-looking, matches the system) and a pure-Go font rasterizer (no new cgo)
before committing. If both fight, the fallback is a pre-baked bitmap font atlas for the
~100 key labels, which is more tedious but has no dependency risk.

### Two decisions for you

1. **Overlay position.** Bottom-centre, floating over the content, is the convention
   (KeyCastr, Screen Studio). The alternative is a strip in the padding area below the
   window, which never occludes content but pushes the output taller. Recommend
   bottom-centre with a configurable inset.
2. **Full text or chords only.** Full text is required for the terminal use case you
   described, so that's the default I've planned for. A later `keystrokes = "chords"` mode
   — showing only `⌘`/`⌃`/`⌥` combinations and special keys, never plain characters —
   would be the safer setting for recording anything involving a login. The `keycode.go`
   design above keeps that cheap to add.
