# Go rewrite

Status: in progress on the `go-rewrite` branch. The Rust code is still the working
version until the Go version reaches parity. Then the Rust is deleted in one commit.

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

```
cmd/recordo/          CLI entry point
internal/camera       virtual camera solve (pure)            ← src/camera.rs
internal/pip          webcam placement (pure)                ← src/pip.rs
internal/pauses       pause log and timeline remap (pure)    ← src/capture/pauses.rs
internal/telemetry    telemetry.json types (pure)            ← src/capture/telemetry.rs (types)
internal/clock        mach time (cgo)                        ← src/capture/clock.rs
internal/config       config.toml                            ← src/config.rs
internal/session      session folders                        ← src/session.rs
internal/capture      SCK, event tap, webcam, AX (cgo + ObjC) ← src/capture/*
internal/render       WebGPU compositor + ffmpeg pipeline    ← src/render/*
internal/tui          Bubble Tea app                         ← src/app/*
```

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

## Environment notes

- The Claude Code sandbox hides the GPU (`MTLCreateSystemDefaultDevice` returns nil).
  Anything that touches WebGPU or capture has to be run outside it.
- Inside the sandbox, Go needs `GOPATH`, `GOCACHE` and `GOMODCACHE` under `$TMPDIR`, and
  `SSL_CERT_FILE=/etc/ssl/cert.pem` to download modules.
