# Go rewrite

Status: done on the `go-rewrite` branch (2026-11-24). The Go build reached parity, was
restructured into vertical slices, and the Rust implementation was deleted.

## Decisions

- **Renderer:** `github.com/cogentcore/webgpu` (wgpu-native through cgo). `shader.wgsl`
  carries over unchanged. The bindings link wgpu-native statically, so the binary depends
  only on system frameworks.
- **Terminal UI:** Bubble Tea, Bubbles and Lip Gloss.
- **macOS APIs** (ScreenCaptureKit, AVFoundation, CGEventTap, Accessibility): small
  Objective-C files next to the Go that uses them, called through cgo. Callbacks from
  dispatch queues hand data to Go through `cgo.Handle`, never by holding Go pointers in
  C.
- **On-disk formats stay compatible:** `config.toml`, `frames.json`, `telemetry.json`,
  `meta.json`, `camera_frames.json` and the session folder layout. A recording made by
  either version renders in the other, which is how each phase is checked.

## Layout

Vertical slices (decided 2026-11-24): code is organised by feature, not by technical
layer. A slice owns its use case, its CLI command and its TUI tab, so changing a feature
touches one folder. Slices import `platform` and `domain`. A slice may call another
slice's exported use case — recording ends with a render, and the recordings tab renders
and compresses — but never its internals or its UI. The TUI shell composes the slices'
views.

```
cmd/recordo/          root command; wires the slices in
internal/features/
  record/             plan → capture → sidecars → health; `record`; TUI record tab
  render/             session → export.mp4 (ffmpeg ⇄ GPU pipeline); `render`; progress view
  compress/           fit an export under a size; `compress`
  recordings/         list, prune, rename, delete; `list`, `prune`; TUI recordings tab
  settings/           config menu/show/get/set/path/edit/reset; TUI settings tab
  doctor/             `doctor`, `windows`, `devices`
internal/tui/         shell only: tabs, layout, key routing
internal/platform/    no feature logic
  macos/              ScreenCaptureKit, event tap, AVFoundation, Accessibility, main loop
  gpu/                WebGPU compositor + shader.wgsl
  ffmpeg/             finding and running ffmpeg/ffprobe
  clock/              mach time
internal/domain/      pure models: camera, pip, pauses, telemetry, frames, session, config
```

Phases 1–4 were built in a layered layout (`internal/capture`, `internal/render`, …) and
moved to slices in three commits once the TUI port landed (dc3dc6d, 7c35ec5, d3ac939).
The TUI shell lives in `internal/tui`, with what tabs share in `internal/tui/kit`;
commands share `internal/cli`.

## Phases

Each phase ends with the Go code checked against the Rust it replaces.

1. **Pure core.** camera, pip, pauses, telemetry, clock, config, session, with every Rust
   unit test ported.
2. **Render.** ffprobe/ffmpeg pipeline, WebGPU compositor, `recordo render <session>`.
   Check: render a session recorded by the Rust version with both, then compare frames
   (`ffmpeg -lavfi psnr`).
3. **Capture.** clock and event tap first, then ScreenCaptureKit recording + frame log,
   window list, web-area AX lookup, devices, webcam. Check: record with Go, render with
   Rust. `recorder::health()` must pass.
4. **CLI and TUI.** All subcommands, then the full-screen app in Bubble Tea.
5. **Cut over.** Update `ci/main.go`, `install.sh`, README and the code tour, then delete
   the Rust.

## Progress

- **Phase 1 done.** All pure-logic packages ported with their tests.
- **Phase 2 done.** Checked against the Rust build on two recordings:

  | Recording | Rust | Go | PSNR vs Rust |
  |---|---|---|---|
  | Safari, webcam, AX page bounds, 20.5s | 7.35s | 6.07s | 83.8 dB avg, 56.9 dB worst |
  | Chrome, a pause, fixed-crop fallback, 43.5s | 14.69s | 11.24s | identical |

  Go is faster because decode, composite and encode overlap on goroutines
  (`internal/render/pipeline.go`); the Rust loop runs them in sequence. The small
  difference on the Safari recording is probably the Go compiler fusing floating-point
  multiply-adds on arm64; this is unverified and far below what is visible. Not yet
  covered: a recording with a voice-over track.

- **Phase 3, part 1 done.** Window list, devices, doctor, Accessibility page bounds,
  telemetry (C threads: 120 Hz poller + event tap) and ScreenCaptureKit recording.
  Checked on 2026-11-24: `doctor` output matches the Rust build; a Go recording
  (display, 10s, 7 clicks, median cursor gap 62ms) renders with the Rust build. Rust and
  Go renders of it differ by 69.5 dB average, 47.8 dB worst — not visible, but larger
  than on the Phase 2 recordings, so worth a crop-level comparison of `camera::solve`
  against `camera.Solve` (not yet done). Not yet covered: a window or browser target, the
  Accessibility path (not granted to the terminal), and voice-over.
- **Phase 3 done.** Webcam recording, frame log and live preview, checked on
  2026-11-24 with the overlay rendered. Fixes found on the way, none of which the Rust
  build has:
  - Capture pins BGRA; macOS's 4:2:0 default made recordings soft.
  - Recording uses the display the window is on, not always the first display.
  - Windows parked out of view (other Space, Stage Manager, a disconnected monitor)
    are never offered or picked by `-a`.
  - A one-off `-webcam` carries into the render; Rust reloads the config and drops it.
  - **The main thread runs macOS's main run loop** (`capture.Main`). AVFoundation
    delivers part of a camera session's work through the main queue; without this,
    `stopRunning` blocks forever. Any Go entry point that uses the webcam must call
    `capture.Main`.
  - `RECORDO_TRACE=1` prints each capture step with thread ids, and dumps goroutine
    stacks if stopping takes over 30s.

- **Phase 4 done.** Full CLI (Cobra, fang, huh) and the Bubble Tea TUI, both verified on
  a real Mac. Recording now waits for `camera.mp4` to start, so a cold camera no longer
  leaves the opening seconds without the overlay.
- **Phase 5 done.** CI, `install.sh`, the README, CONTRIBUTING, SECURITY and the code tour
  describe the Go build; the Rust implementation is deleted (bb8ca8f). The no-network
  check now reads the binary's Go symbols instead of socket imports, which every Go binary
  on macOS declares.

## Environment notes

- The Claude Code sandbox hides the GPU (`MTLCreateSystemDefaultDevice` returns nil).
  Anything that touches WebGPU or capture has to be run outside it.
- Inside the sandbox, Go needs `GOPATH`, `GOCACHE` and `GOMODCACHE` under `$TMPDIR`, and
  `SSL_CERT_FILE=/etc/ssl/cert.pem` to download modules.
