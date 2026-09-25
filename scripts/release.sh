#!/bin/bash
# Prepares a tagged release and prints the Homebrew formula for it.
#
# Exists because of a chicken-and-egg problem: a formula needs the sha256 of the release
# tarball, and that tarball does not exist until the tag is pushed. Doing it by hand
# means tagging, waiting, downloading, hashing and pasting — easy to get subtly wrong,
# and the failure shows up only when someone tries to install.
#
# The tag is created locally but never pushed. Pushing is yours.
set -euo pipefail

REPO="waarispierre/recordo"
VERSION="${1:-}"

die() { echo "error: $*" >&2; exit 1; }

[[ -n "$VERSION" ]] || die "usage: $0 <version>   e.g. $0 0.1.0"
[[ "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "version must look like 1.2.3, got '$VERSION'"

TAG="v$VERSION"

# A release built from a dirty tree is not the release anyone can reproduce.
[[ -z "$(git status --porcelain)" ]] || die "working tree is dirty; commit or stash first"

manifest_version=$(grep -m1 '^version = ' Cargo.toml | cut -d'"' -f2)
[[ "$manifest_version" == "$VERSION" ]] || \
    die "Cargo.toml says $manifest_version but you asked for $VERSION — update the manifest first"

git rev-parse "$TAG" >/dev/null 2>&1 && die "tag $TAG already exists"

echo "==> running checks"
go run ci/main.go

# The formula's stable url is fetched by curl, which has no GitHub credentials. A private
# repository therefore cannot have a stable release, only a head-only formula.
echo "==> checking repository visibility"
visibility=$(curl -s -o /dev/null -w '%{http_code}' "https://api.github.com/repos/$REPO")
if [[ "$visibility" != "200" ]]; then
    cat >&2 <<EOF

error: https://github.com/$REPO is not publicly readable (HTTP $visibility).

  Homebrew fetches a formula's stable tarball with curl, which holds no GitHub
  credentials, so a private repository's tarball returns 404 at install time.

  While the repository is private, use the head-only formula already in
  packaging/homebrew/recordo.rb:

      brew install --HEAD waarispierre/tap/recordo

  Re-run this script once the repository is public.
EOF
    exit 1
fi

echo "==> tagging $TAG"
git tag -a "$TAG" -m "recordo $VERSION"

echo "==> push the tag, then re-run to get the formula:"
echo "      git push origin main $TAG"
echo

# The hash can only be computed once GitHub can serve the tarball for the tag.
TARBALL="https://github.com/$REPO/archive/refs/tags/$TAG.tar.gz"
if ! curl -sfL -o /dev/null "$TARBALL" 2>/dev/null; then
    echo "note: $TAG is not on GitHub yet, so the sha256 cannot be computed."
    echo "      Push the tag and run: $0 $VERSION --formula"
    exit 0
fi

SHA=$(curl -sL "$TARBALL" | shasum -a 256 | cut -d' ' -f1)
cat <<EOF

==> stable formula stanza — replace the head line in Formula/recordo.rb with:

  url "$TARBALL"
  sha256 "$SHA"

EOF
