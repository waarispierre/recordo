# typed: strict
# frozen_string_literal: true

# Homebrew formula for recordo.
#
# Copy into a tap repository — github.com/waarispierre/homebrew-tap — at
# Formula/recordo.rb.
#
# Head-only until the first tagged release. The repository is public, so no credentials
# are involved:
#
#     brew install --HEAD waarispierre/tap/recordo
#
# scripts/release.sh adds the stable `url` and `sha256` once a version is tagged, after
# which plain `brew install` works and `brew upgrade` can see new versions.
class Recordo < Formula
  desc "Screen recordings with a cursor-following camera"
  homepage "https://github.com/waarispierre/recordo"
  license any_of: ["MIT", "Apache-2.0"]

  head "https://github.com/waarispierre/recordo.git", branch: "main"

  depends_on "rust" => :build
  # ffmpeg is executed as a subprocess, never linked, so its GPL licence does not reach
  # this formula's binary. Declaring it here is what makes installation one command.
  depends_on "ffmpeg"
  # macOS 26, not 15. SCRecordingOutput needs only macOS 15, but building requires a
  # macOS 26 SDK: the apple-metal crate's Swift bridge references MTLSamplerReductionMode
  # and MTLSamplerDescriptor.lodBias behind `if #available(macOS 26.0, *)`, and that
  # runtime guard does not stop the compiler needing the symbols. Declaring :sequoia here
  # would let Homebrew start a build that cannot succeed.
  depends_on macos: :tahoe

  def install
    # SwiftPM evaluates Package.swift inside its own sandbox-exec, and Homebrew has
    # already wrapped this build in one. sandbox-exec cannot nest, so the Swift bridge in
    # screencapturekit fails with "sandbox_apply: Operation not permitted" — a failure
    # that only happens under brew, never in a normal cargo build.
    #
    # `swift build --disable-sandbox` avoids it, but that crate's build.rs calls `swift`
    # with hardcoded arguments and reads no environment variable, so the only way to add
    # the flag is to shim the binary on PATH. Homebrew offers no way to disable its own
    # sandbox for a formula build: HOMEBREW_NO_SANDBOX covers casks and Linux only.
    shim = buildpath/"brew-swift-shim"
    shim.mkpath
    (shim/"swift").write <<~BASH
      #!/bin/bash
      # Only `swift build` accepts --disable-sandbox; other subcommands reject it.
      if [ "$1" = "build" ]; then
          shift
          exec /usr/bin/swift build --disable-sandbox "$@"
      fi
      exec /usr/bin/swift "$@"
    BASH
    chmod 0755, shim/"swift"
    ENV.prepend_path "PATH", shim

    system "cargo", "install", *std_cargo_args
  end

  def caveats
    <<~EOS
      recordo needs macOS permissions, granted to the terminal you run it from:

        Screen Recording   required — quit and reopen your terminal afterwards
        Input Monitoring   optional — enables click-triggered zoom
        Accessibility      optional — finds a browser's exact page bounds

      Check them at any time with:
        recordo doctor
    EOS
  end

  test do
    assert_match "recordo", shell_output("#{bin}/recordo --version")
    # Exercises config creation and the TOML round-trip without needing any TCC grant.
    assert_match "zoom_percent", shell_output("#{bin}/recordo config show")
  end
end
