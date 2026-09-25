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

## While the repository is private

Homebrew can work with a private repo, but only partly, and the halves fail differently.

**The tap can be private.** `brew tap` accepts an explicit URL over any transport git
understands, so SSH keys do the authenticating:

```sh
brew tap waarispierre/tap git@github.com:waarispierre/homebrew-tap.git
```

**The source tarball cannot.** A formula's `url` is fetched with curl, which has no GitHub
credentials, so `https://github.com/.../archive/refs/tags/v0.1.0.tar.gz` returns 404 for a
private repo. Stable releases therefore do not work while the source is private.

The way round it is a head-only install, which clones over git instead of fetching a
tarball, so SSH handles auth:

```ruby
head "git@github.com:waarispierre/recordo.git", branch: "main"
```

```sh
brew install --HEAD waarispierre/tap/recordo
```

That works, but every install builds `main` rather than a pinned release, and `brew
upgrade` will not see new versions.

**Honestly, while it is private this is more machinery than it is worth.** A direct
install needs no tap at all:

```sh
brew install ffmpeg
cargo install --git ssh://git@github.com/waarispierre/recordo.git
```

Switch to the tap when the repository goes public and the tarball URL starts resolving.

## A prerequisite worth knowing

Homebrew refuses to build when the Command Line Tools are older than the OS, with:

```
Error: Your Command Line Tools are too outdated.
```

It is stricter than cargo, which builds happily on a mismatched pair. If you see this,
update the Command Line Tools from Software Update, or:

```sh
sudo rm -rf /Library/Developer/CommandLineTools
sudo xcode-select --install
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
