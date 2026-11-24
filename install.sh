#!/bin/bash
# Builds and installs recordo from a clone of this repository.
#
#     git clone https://github.com/waarispierre/recordo.git
#     cd recordo
#     ./install.sh
#
# Deliberately a script rather than a package manager. recordo is early, macOS-only and
# has one binary; a Homebrew formula would add a tap repository to maintain and a release
# process. None of that earns its place yet. With Go set up, `go install` works too — see
# the README.
set -euo pipefail

BOLD=$'\e[1m'; DIM=$'\e[90m'; RED=$'\e[31m'; GREEN=$'\e[32m'; YELLOW=$'\e[33m'; OFF=$'\e[0m'

ok()   { printf '  %s✓%s %s\n' "$GREEN" "$OFF" "$1"; }
warn() { printf '  %s!%s %s\n' "$YELLOW" "$OFF" "$1"; }
die()  { printf '\n  %s✗%s %s\n\n' "$RED" "$OFF" "$1" >&2; exit 1; }

printf '\n  %srecordo%s %sinstaller%s\n\n' "$BOLD" "$OFF" "$DIM" "$OFF"

# --- prerequisites -----------------------------------------------------------------
# Checked up front and all at once: finding out about a missing dependency halfway
# through a build is a poor way to learn it.

[[ "$(uname -s)" == "Darwin" ]] || die "recordo is macOS-only; this is $(uname -s)."

macos_major=$(sw_vers -productVersion | cut -d. -f1)
if (( macos_major < 15 )); then
    die "macOS 15 or later is required, found $(sw_vers -productVersion).

  Not an arbitrary floor: recording writes straight to a file through ScreenCaptureKit's
  SCRecordingOutput, which macOS 15 introduced."
fi
ok "macOS $(sw_vers -productVersion)"

# cgo compiles the Objective-C that talks to ScreenCaptureKit and AVFoundation, so a C
# toolchain and the macOS SDK are needed; the Command Line Tools provide both.
if ! xcode-select -p >/dev/null 2>&1; then
    die "Xcode Command Line Tools are missing. Install them with:

      xcode-select --install"
fi
ok "Command Line Tools $(xcode-select -p)"

if ! command -v go >/dev/null 2>&1; then
    die "Go is not installed. Install it with:

      brew install go

  or from https://go.dev/dl"
fi
# An older Go is fine: it fetches the toolchain go.mod asks for by itself.
ok "$(go version | cut -d' ' -f3)"

# ffmpeg is a runtime dependency, not a build one, so a missing copy is a warning here
# and an error later. Saying so now is kinder than a failed first render.
if command -v ffmpeg >/dev/null 2>&1 && command -v ffprobe >/dev/null 2>&1; then
    ok "ffmpeg $(ffmpeg -version 2>/dev/null | head -1 | cut -d' ' -f3)"
else
    warn "ffmpeg not found — recording works, rendering will not. Install it with:"
    printf '      %sbrew install ffmpeg%s\n' "$DIM" "$OFF"
fi

# --- build -------------------------------------------------------------------------

cd "$(dirname "$0")"
version=$(git describe --tags --always --dirty 2>/dev/null || echo dev)
bin_dir=$(go env GOBIN)
[[ -n "$bin_dir" ]] || bin_dir="$(go env GOPATH)/bin"
mkdir -p "$bin_dir"

printf '\n  %sbuilding%s %s%s (the first build fetches dependencies)%s\n\n' \
    "$BOLD" "$OFF" "$DIM" "$version" "$OFF"

CGO_ENABLED=1 go build -trimpath \
    -ldflags "-s -w -X github.com/waarispierre/recordo/internal/cli.Version=$version" \
    -o "$bin_dir/recordo" ./cmd/recordo

# --- report ------------------------------------------------------------------------

ok "installed to $bin_dir/recordo"

case ":$PATH:" in
    *":$bin_dir:"*) ;;
    *)
        warn "$bin_dir is not on your PATH. Add it to your shell profile:"
        # shellcheck disable=SC2016  # $PATH is meant literally: this line is to be pasted.
        printf '      %sexport PATH="%s:$PATH"%s\n' "$DIM" "$bin_dir" "$OFF"
        ;;
esac

cat <<EOF

  ${BOLD}next${OFF}
    recordo doctor    ${DIM}check macOS permissions${OFF}
    recordo           ${DIM}open the app and record something${OFF}

  recordo needs Screen Recording permission, granted to the terminal you run it
  from. ${DIM}doctor${OFF} shows what is missing.

EOF
