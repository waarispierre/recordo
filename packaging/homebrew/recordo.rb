# typed: strict
# frozen_string_literal: true

# Homebrew formula for recordo.
#
# Copy into a tap repository — github.com/waarispierre/homebrew-tap — at
# Formula/recordo.rb.
#
# This is deliberately head-only: it has no `url`/`sha256` stable release. A formula's
# stable URL is fetched with curl, which holds no GitHub credentials, so a private
# repository's tarball 404s. `head` clones over git instead, where SSH authenticates —
# which makes this installable while the source stays private.
#
#     brew install --HEAD waarispierre/tap/recordo
#
# Once the repository is public, scripts/release.sh adds the stable url and sha256, and
# plain `brew install` starts working.
class Recordo < Formula
  desc "Screen recordings with a cursor-following camera"
  homepage "https://github.com/waarispierre/recordo"
  license any_of: ["MIT", "Apache-2.0"]

  # SSH rather than HTTPS so a private repository authenticates with your existing keys.
  head "git@github.com:waarispierre/recordo.git", branch: "main"

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
