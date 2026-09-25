# Contributing

## Getting set up

```sh
brew install ffmpeg
cargo build
cargo test
```

**macOS 26+ is required to build.** Two separate limits, worth not conflating:

- `SCRecordingOutput`, used to write the capture, needs macOS 15 at *runtime*.
- The `apple-metal` crate's Swift bridge references `MTLSamplerReductionMode` and
  `MTLSamplerDescriptor.lodBias` behind `if #available(macOS 26.0, *)`. That guard is a
  runtime check, so the code still has to compile against an SDK that has those symbols.
  A macOS 15 SDK therefore fails to build, despite `apple-metal` declaring `.macOS(.v11)`
  in its `Package.swift` — an upstream inaccuracy.

The build minimum is the binding one, so it is what CI and `install.sh` check.

You need Xcode Command Line Tools, but not full Xcode — `build.rs` points the linker at
the Swift runtime that ships with the Command Line Tools.

Running the tool needs Screen Recording permission on your terminal. `recordo doctor`
reports what is missing.

## Before opening a pull request

Run the whole of CI locally — the same program GitHub runs:

```sh
go run ci/main.go
```

Needs only Go; takes about twenty seconds.

```sh
go run ci/main.go -only=portable   # format, licences, dependency policy, shell lint
go run ci/main.go -only=native     # clippy, tests, release build, no-network assertion
```

### Why the pipeline is split

recordo compiles only on macOS: it links ScreenCaptureKit, builds a Swift shim and
renders through Metal. Anything needing a build is therefore macOS-only, while the
checks that read source and metadata run anywhere. On Linux the native half is skipped
with a notice rather than a confusing compile error.

Two optional tools are used if present:

```sh
cargo install cargo-deny --locked   # licence and dependency policy
brew install shellcheck             # shell linting
```

Missing either one **skips** that check locally but **fails** it on CI, so the pipeline
cannot quietly pass with half its checks unrun.

If you prefer the underlying commands:

```sh
cargo fmt --all
cargo clippy --all-targets   # must be clean, CI denies warnings
cargo test
./scripts/check-local-only.sh
```

## Things that are deliberate

A few decisions look odd without context and should not be "fixed" casually.

**Nothing may reach the network.** There is no analytics, no update check, no crash
reporting, and no HTTP client in the dependency graph. `scripts/check-local-only.sh`
enforces this against the built binary and runs in CI. A change that adds a networking
dependency will fail the build, by design.

**The event tap is mouse-only.** No keyboard event type is registered, so it cannot
observe keystrokes even when Input Monitoring is granted. Cursor position is polled
separately and needs no permission at all. Adding keyboard capture would need an explicit,
default-off opt-in and a clear indication while recording.

**FFmpeg is executed, never linked.** Homebrew's FFmpeg is GPL. Calling it as a
subprocess keeps the licences separate; linking `ffmpeg-next` or bundling a binary would
not. `deny.toml` bans the relevant crates so this cannot happen by accident.

**Frames correlate to cursor samples by mach timestamp, never by frame index.** The
capture queue is serial and drops frames under load, so indices drift from wall clock.
This is the single easiest thing to get subtly wrong.

**Style lengths are in points, not pixels.** They are scaled by the capture's backing
factor at render time. Expressing them in pixels makes the result look different on
Retina and non-Retina displays.

**One renderer, not two.** Effects exist only in the WGSL shader. A second, faster
preview implementation in another language would drift from it — a bug class worth
avoiding entirely.

## Structure

The repository separates the application from the machinery around it:

```
src/          the application
ci/           the CI runner, the same one GitHub runs
install.sh    what users run after cloning
scripts/      standalone tools, also invoked by ci/
docs/         code tour and proposals
```

Within `src/`, the layout follows the three layers the app is built from:

| Path | Responsibility |
|---|---|
| `src/capture/` | Everything platform-specific: ScreenCaptureKit frames, the cursor tap, window selection, browser page bounds |
| `src/camera.rs` | Telemetry to a per-frame crop rectangle — the motion model |
| `src/render/` | GPU compositing (`mod.rs`, `shader.wgsl`) and the decode/encode pipeline (`export.rs`) |
| `src/app/` | Full-screen TUI and line-oriented output. Binary-only |
| `src/config.rs`, `session.rs`, `tools.rs` | Settings, where recordings live, helper-binary resolution |

`src/capture/` is the only layer that knows it is on macOS. A Windows port would add
siblings there and leave the rest untouched.

`src/camera.rs` is pure maths with no OS or GPU dependency, so it is the one part that can
be tested properly — and the part where the product's perceived quality actually lives.
Changes there should come with tests.

There is a longer walkthrough in [docs/code-tour.md](docs/code-tour.md).

## Reporting a security issue

Please open a private security advisory on GitHub rather than a public issue.
[SECURITY.md](SECURITY.md) documents the properties this project guarantees and the
limitations it does not hide.
