# Homebrew formula for recordo.
#
# Copy this into a tap repository — github.com/waarispierre/homebrew-tap — at
# Formula/recordo.rb. Users then install with:
#
#     brew install waarispierre/tap/recordo
#
# Built from source deliberately. A locally compiled binary is not quarantined by
# Gatekeeper, so no Apple Developer account, code signing or notarization is needed.
class Recordo < Formula
  desc "Screen recordings with a cursor-following camera"
  homepage "https://github.com/waarispierre/recordo"
  url "https://github.com/waarispierre/recordo/archive/refs/tags/v0.1.0.tar.gz"
  # Replace after tagging:  shasum -a 256 <downloaded tarball>
  sha256 "REPLACE_WITH_TARBALL_SHA256"
  license any_of: ["MIT", "Apache-2.0"]
  head "https://github.com/waarispierre/recordo.git", branch: "main"

  depends_on "rust" => :build
  # ffmpeg is executed as a subprocess, never linked, so its GPL licence does not reach
  # this formula's binary. Declaring it here is what makes installation one command.
  depends_on "ffmpeg"
  # SCRecordingOutput, used to write the capture, is macOS 15+.
  depends_on macos: :sequoia

  def install
    system "cargo", "install", *std_cargo_args(path: "cli")
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
