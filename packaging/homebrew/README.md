# Publishing to Homebrew

`recordo` installs from a personal tap. There is nothing to submit to homebrew-core and
no binaries to sign.

## One-time setup

1. Create a public repository named **`homebrew-tap`** under your GitHub account. The
   `homebrew-` prefix is required; Homebrew derives the tap name from it.
2. Copy `recordo.rb` into it as `Formula/recordo.rb`.

## Each release

```sh
git tag v0.1.0
git push origin v0.1.0

# Homebrew needs the checksum of the tarball GitHub generates for the tag.
curl -sL https://github.com/waarispierre/recordo/archive/refs/tags/v0.1.0.tar.gz \
  | shasum -a 256
```

Update `url` and `sha256` in the formula, commit the tap, done. Users get:

```sh
brew install waarispierre/tap/recordo
```

## Why build from source

A Homebrew *bottle* (prebuilt binary) would install in seconds rather than minutes, but
macOS quarantines downloaded binaries unless they are signed and notarized — which needs
a paid Apple Developer account and a notarization step in CI. A source build sidesteps
all of it: the binary is produced locally, so Gatekeeper never quarantines it.

The tradeoff is build time. `wgpu` and `naga` are large, so expect a few minutes.

## Permissions are not affected by this choice

`recordo` is a command-line tool, so macOS attributes its TCC grants to the **terminal
application** that runs it, not to the `recordo` binary. Signing would make no difference
to how permissions behave.
