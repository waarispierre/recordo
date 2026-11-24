# Security

recordo records your screen, so it is worth being precise about what it does with that
recording. The short version: **nothing leaves your machine**, and the tool cannot see
what you type.

## Reporting a vulnerability

Please open a [private security advisory](https://github.com/waarispierre/recordo/security/advisories/new)
rather than a public issue. I will acknowledge within a few days.

## What recordo does with your data

**Nothing is uploaded.** There is no account, no cloud, no telemetry, no analytics, no
crash reporting and no update check. There is no HTTP client anywhere in the dependency
graph.

This is not a promise you have to take on trust:

```sh
go build -o bin/recordo ./cmd/recordo && ./scripts/check-local-only.sh bin/recordo
```

That script asserts, against the compiled binary, that no networking framework is linked,
no URL-loading symbol is imported, and no dialing, listening, resolving, connection, HTTP
or TLS code survived the linker. CI separately fails the moment a networking package
enters the dependency graph. Both run on every change, so a dependency that would break
the guarantee fails the build.

**Recordings stay in `~/Movies/Recordo/`**, in per-recording folders created `0700`, with
every file written `0600`. Other accounts on the same Mac cannot read them.

**Settings live in `~/Library/Application Support/recordo/`**, same permissions.

## recordo cannot see your keystrokes

The event tap registers mouse and scroll events only — `LeftMouseDown`, `RightMouseDown`,
`OtherMouseDown`, the corresponding `Up` events, and `ScrollWheel`. No keyboard event type
is in the mask, so keystrokes are invisible to it **even when Input Monitoring is
granted**.

Cursor *position* is polled rather than tapped, and needs no permission at all. If you
deny Input Monitoring, recording still works — you lose click-triggered zoom, nothing
else.

There is a [proposal for a keystroke overlay](docs/proposals/keystroke-overlay.md). It is
not implemented and not planned. If it is ever built, this section changes in the same
commit.

## Permissions, and why each is asked for

| Permission | Why | If denied |
|---|---|---|
| Screen Recording | Required to capture anything | recordo fails with a clear error |
| Input Monitoring | Detect clicks, to trigger zoom | Records and pans, never click-zooms |
| Accessibility | Read a browser's exact page bounds | Browser pages cropped by a fixed guess |
| Microphone | Voice over | recordo fails with a clear error, only asked when `--mic` or `audio.microphone` is on |
| Camera | Webcam overlay | recording continues screen-only, only asked when `--webcam` or `webcam.enabled` is on |

These are granted to the **terminal you run recordo from**, not to the binary — that is
how macOS attributes permissions for command-line tools. `recordo doctor` shows what is
currently granted.

## Privacy features

For browsers, recordo keeps only the web page and draws a synthetic window frame around
it. The recording therefore shows no tabs, bookmarks, profile avatar or URL history. Page
bounds are read from the browser itself via the Accessibility API, so they stay correct
when a bookmarks bar is toggled.

This redacts the browser chrome, **not the page**. Anything visible in the page itself is
recorded as-is.

## Known limitations

Stated plainly rather than omitted.

- **`capture.mp4` is unredacted.** The raw capture retains the real browser chrome that
  the render removes. It is kept so you can re-render without re-recording, which means
  the unredacted version stays on disk until you remove it:

  ```sh
  recordo prune                  # drop raw captures, keep rendered videos
  recordo prune --older-than 7
  recordo prune --all            # remove recordings entirely
  ```

- **`telemetry.json` records global cursor coordinates**, including movement outside the
  recorded window. Local-only and `0600`, but it is more than the recorded region.

- **The socket functions are declared, though nothing can call them.** On macOS, Go's
  standard `syscall` package declares the libc socket functions in every Go binary,
  networking or not, and `spf13/pflag` (the flag parser) imports Go's `net` package to
  parse IP-address flags, which recordo does not define. The linker keeps only `net`'s
  inert parts — IP constants and initialisation — and `check-local-only.sh` fails the
  build if any dialer, listener, resolver or connection code survives, or if any package
  other than `pflag` imports `net`. So "recordo never opens a socket" is accurate and
  checked on every change, while "this binary contains no reference to a socket function"
  is not, and the difference is worth stating.

- **FFmpeg is a large parser handling your capture.** recordo invokes it with
  `-protocol_whitelist file,pipe,fd`, so it cannot follow a remote reference embedded in
  a media file. That is the proportionate mitigation for locally-produced input.

- **Voice over is off by default, and it is just audio.** `audio.microphone` (or `--mic`
  for a single run) records your voice — never system audio, so a notification chime or
  another app's sound never lands in the recording. The audio is embedded in `capture.mp4`
  and carried into `export.mp4`, both `0600` like the rest of the recording, and both
  removed by `recordo prune --all`. `recordo doctor` reports the current Microphone grant
  without ever triggering the permission prompt itself. This does not change the
  keystrokes guarantee above — nothing here observes the keyboard.

- **The webcam overlay is off by default, and `camera.mp4` is a raw recording of you.**
  `webcam.enabled` (or `--webcam` for a single run) writes a second file, `camera.mp4`,
  alongside `capture.mp4` — `0600`, same as everything else, and removed by
  `recordo prune` along with the rest of the raw capture (not just `--all`: an
  unredacted recording of your face is at least as sensitive as unredacted browser
  chrome). `recordo doctor` reports the current Camera grant without triggering the
  permission prompt. A camera that is missing, in use, or denied degrades the recording
  to screen-only rather than failing it.

## Licensing boundary

recordo **executes** `ffmpeg` as a subprocess and never links it, so FFmpeg's GPL licence
does not extend to this project, and no FFmpeg binary is bundled or redistributed here.
CI reads every Go dependency's licence and fails on any copyleft licence, so this cannot
change by accident.

Video is encoded with `h264_videotoolbox`, the hardware encoder built into macOS, so H.264
patent licensing sits with the operating system rather than with this project.
