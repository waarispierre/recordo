# Contributing

## Getting set up

```sh
brew install go ffmpeg
go build ./...
go test ./...
```

**macOS 15+ is required.** Recording writes straight to a file through ScreenCaptureKit's
`SCRecordingOutput`, which macOS 15 introduced. The webcam's exact file-start time uses an
AVFoundation callback from macOS 15.2; on 15.0–15.1 it falls back gracefully.

You need the Xcode Command Line Tools, not full Xcode: cgo compiles the Objective-C under
`internal/platform/macos` against the SDK they provide.

Running the tool needs Screen Recording permission on your terminal. `recordo doctor`
reports what is missing. `RECORDO_TRACE=1` prints each capture step with thread ids, and
dumps every goroutine's stack if stopping a recording stalls.

## Before opening a pull request

Run the whole of CI locally — the same program GitHub runs:

```sh
go run ./ci
```

```sh
go run ./ci -only=portable   # format, module checksums, licences, dependency policy, shell lint
go run ./ci -only=native     # vet, tests with -race, build, no-network assertion
```

### Why the pipeline is split

recordo compiles only on macOS: it calls ScreenCaptureKit and AVFoundation through cgo
and renders through Metal. Anything needing a build is therefore macOS-only, while the
checks that read source and metadata run anywhere. On Linux the native half is skipped
with a notice rather than a confusing compile error.

One optional tool is used if present:

```sh
brew install shellcheck             # shell linting
```

Missing it **skips** that check locally but **fails** it on CI, so the pipeline cannot
quietly pass with a check unrun.

If you prefer the underlying commands:

```sh
gofmt -l cmd internal ci     # must print nothing
go vet ./...
go test -race ./...
go build -o bin/recordo ./cmd/recordo && ./scripts/check-local-only.sh bin/recordo
```

## Things that are deliberate

A few decisions look odd without context and should not be "fixed" casually.

**Nothing may reach the network.** There is no analytics, no update check, no crash
reporting, and no HTTP client in the dependency graph. CI fails if a networking package
enters it, and `scripts/check-local-only.sh` checks the built binary keeps no dialing,
resolving, HTTP or TLS code. A change that adds a networking dependency will fail the
build, by design.

**The event tap is mouse-only.** No keyboard event type is registered, so it cannot
observe keystrokes even when Input Monitoring is granted. Cursor position is polled
separately and needs no permission at all. Adding keyboard capture would need an explicit,
default-off opt-in and a clear indication while recording.

**FFmpeg is executed, never linked.** Homebrew's FFmpeg is GPL. Calling it as a
subprocess keeps the licences separate; linking it or bundling a binary would not. CI's
licence check fails on any copyleft dependency, so this cannot happen by accident.

**Frames correlate to cursor samples by mach timestamp, never by frame index.** The
capture queue is serial and drops frames under load, so indices drift from wall clock.
This is the single easiest thing to get subtly wrong.

**Camera sessions run on the main thread.** AVFoundation delivers part of a capture
session's work through the main dispatch queue, which a Go program never services by
itself; without it, stopping the webcam hangs forever. `macos.Main` gives the main thread
to macOS's run loop and runs the program on a goroutine. Any new entry point that might
touch the webcam must go through it.

**Capture is pinned to full-resolution BGRA.** On recent macOS, ScreenCaptureKit defaults
to 4:2:0 YUV, which halves colour resolution and makes text visibly soft.

**Style lengths are in points, not pixels.** They are scaled by the capture's backing
factor at render time. Expressing them in pixels makes the result look different on
Retina and non-Retina displays.

**One renderer, not two.** Effects exist only in the WGSL shader. A second, faster
preview implementation would drift from it — a bug class worth avoiding entirely.

## Structure

The code is organised as **vertical slices**: one folder per feature, holding its logic,
its command and its screen in the app. Changing a feature should mean touching one folder.

```
cmd/recordo/          assembles the root command from the slices
internal/features/    one slice per feature
  record/             what to capture, capturing it, sidecars, health; `record`; record tab
  render/             capture + sidecars → export.mp4; `render`
  compress/           fit an export under a size; `compress`
  recordings/         list, prune, rename, delete; recordings tab
  settings/           config.toml; `config`; settings tab
  doctor/             `doctor`, `windows`, `devices`
internal/tui/         the full-screen app's shell: tabs, layout, key routing
internal/cli/         shared kit for commands: global flags, styles, config loading
internal/platform/    talks to the machine; no feature logic
  macos/              ScreenCaptureKit, event tap, AVFoundation, Accessibility (cgo + Objective-C)
  gpu/                WebGPU compositor and the WGSL shader
  ffmpeg/             finding and probing ffmpeg/ffprobe
  clock/              mach time
internal/domain/      pure models with no OS or GPU dependency
ci/                   the CI runner, the same one GitHub runs
scripts/              standalone tools, also invoked by ci/
```

The rules that keep slices independent:

- A slice imports `domain`, `platform` and the shared kits.
- A slice may call another slice's **exported use case** — recording ends with
  `render.Run`, and the recordings tab offers render and compress — but never another
  slice's internals or its screen.
- `platform` knows nothing about features; `domain` knows nothing about the OS.

`internal/platform/macos` is the only code that knows it is on macOS. A Windows port
would add a sibling there and leave the rest untouched.

`internal/domain/camera` is pure maths with no OS or GPU dependency, so it is the part
that can be tested properly — and the part where the product's perceived quality actually
lives. Changes there should come with tests.

There is a longer walkthrough in [docs/code-tour.md](docs/code-tour.md).

## Reporting a security issue

Please open a private security advisory on GitHub rather than a public issue.
[SECURITY.md](SECURITY.md) documents the properties this project guarantees and the
limitations it does not hide.
