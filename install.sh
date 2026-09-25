#!/bin/bash
# Builds and installs recordo from a clone of this repository.
#
#     git clone https://github.com/waarispierre/recordo.git
#     cd recordo
#     ./install.sh
#
# Deliberately a script rather than a package manager. recordo is early, macOS-only and
# has one binary; a Homebrew formula added a tap repository to maintain, a release
# process, and a workaround for SwiftPM's sandbox that traded away a layer of isolation.
# None of that earns its place yet.
set -euo pipefail

BOLD=$'\e[1m'; DIM=$'\e[90m'; RED=$'\e[31m'; GREEN=$'\e[32m'; YELLOW=$'\e[33m'; OFF=$'\e[0m'

ok()   { printf '  %s✓%s %s\n' "$GREEN" "$OFF" "$1"; }
warn() { printf '  %s!%s %s\n' "$YELLOW" "$OFF" "$1"; }
die()  { printf '\n  %s✗%s %s\n\n' "$RED" "$OFF" "$1" >&2; exit 1; }

printf '\n  %srecordo%s %sinstaller%s\n\n' "$BOLD" "$OFF" "$DIM" "$OFF"

# --- prerequisites -----------------------------------------------------------------
# Checked up front and all at once: finding out about a missing dependency four minutes
# into a compile is a poor way to learn it.

[[ "$(uname -s)" == "Darwin" ]] || die "recordo is macOS-only; this is $(uname -s)."

macos_major=$(sw_vers -productVersion | cut -d. -f1)
if (( macos_major < 26 )); then
    die "macOS 26 or later is required to build, found $(sw_vers -productVersion).

  Not an arbitrary floor: the apple-metal crate's Swift bridge references
  MTLSamplerReductionMode behind 'if #available(macOS 26.0, *)'. That guard is a runtime
  check, so the symbols still have to exist in the SDK at compile time."
fi
ok "macOS $(sw_vers -productVersion)"

if ! xcode-select -p >/dev/null 2>&1; then
    die "Xcode Command Line Tools are missing. Install them with:

      xcode-select --install"
fi
ok "Command Line Tools $(xcode-select -p)"

if ! command -v cargo >/dev/null 2>&1; then
    die "Rust is not installed. Get it from https://rustup.rs:

      curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh"
fi
ok "cargo $(cargo --version | cut -d' ' -f2)"

# ffmpeg is a runtime dependency, not a build one, so a missing copy is a warning here
# and an error later. Saying so now is kinder than a failed first render.
if command -v ffmpeg >/dev/null 2>&1 && command -v ffprobe >/dev/null 2>&1; then
    ok "ffmpeg $(ffmpeg -version 2>/dev/null | head -1 | cut -d' ' -f3)"
else
    warn "ffmpeg not found — recording works, rendering will not. Install it with:"
    printf '      %sbrew install ffmpeg%s\n' "$DIM" "$OFF"
fi

# --- build -------------------------------------------------------------------------

printf '\n  %sbuilding%s %s(a few minutes; wgpu and naga are large)%s\n\n' \
    "$BOLD" "$OFF" "$DIM" "$OFF"

cd "$(dirname "$0")"
cargo install --path . --locked

# --- report ------------------------------------------------------------------------

bin_dir="${CARGO_HOME:-$HOME/.cargo}/bin"
printf '\n'
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
    recordo           ${DIM}record something${OFF}

  recordo needs Screen Recording permission, granted to the terminal you run it
  from. ${DIM}doctor${OFF} shows what is missing.

EOF
