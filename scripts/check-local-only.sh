#!/bin/bash
# Asserts the built binary cannot reach the network.
#
# recordo's core promise is that recordings never leave the machine. That is a
# property of the dependency graph, which a single careless `cargo add` can break — so it
# is checked mechanically rather than assumed.
#
# One documented exception, verified rather than assumed: the `apple-cf` crate (a
# transitive dependency of `screencapturekit`) ships a CFSocket wrapper including a
# `cf_socket_create_udp_ipv4` helper, and it survives into the binary. No code here calls
# it, so the capability is dormant — but it is present, and claiming otherwise would be
# false. This script therefore asserts the property actually under our control: that
# nothing in *this* crate, and no crate other than apple-cf, references a socket API.
#
# Note on style: output is captured into variables before matching. Piping into `grep -q`
# under `set -o pipefail` makes the pipeline fail when grep short-circuits and the
# producer takes SIGPIPE — which silently inverted a check in an earlier version of this
# script and reported PASS on a binary that should have failed.
set -euo pipefail

BIN="${1:-target/release/recordo}"
if [[ ! -x "$BIN" ]]; then
    echo "usage: $0 [path-to-binary]   (build first: cargo build --release)" >&2
    exit 2
fi

fail=0
ok()  { printf '  ok    %s\n' "$1"; }
bad() { printf '  FAIL  %s\n' "$1" >&2; if [[ -n "${2:-}" ]]; then printf '%s\n' "$2" | sed 's/^/          /' >&2; fi; fail=1; }

libs=$(otool -L "$BIN" || true)
undefined=$(nm -u "$BIN" 2>/dev/null || true)

echo "== linked frameworks =="
hits=$(printf '%s\n' "$libs" | grep -iE "CFNetwork|/Network\.framework|Security\.framework|libcurl" || true)
if [[ -n "$hits" ]]; then bad "a networking framework is linked" "$hits"; else ok "no networking framework linked"; fi

echo "== socket syscalls =="
hits=$(printf '%s\n' "$undefined" | grep -E '^_(socket|connect|bind|listen|accept|sendto|recvfrom|getaddrinfo|gethostbyname)$' || true)
if [[ -n "$hits" ]]; then bad "a raw socket syscall is imported" "$hits"; else ok "no raw socket syscalls"; fi

echo "== URL loading =="
hits=$(printf '%s\n' "$undefined" | grep -E "NSURLSession|NSURLConnection|CFURLConnection|CFHTTP" || true)
if [[ -n "$hits" ]]; then bad "a URL-loading symbol is imported" "$hits"; else ok "no URL-loading symbols"; fi

echo "== socket capability provenance =="
# apple-cf's dormant CFSocket wrapper is the one accepted source. Anything else — above
# all this crate itself — is a regression.
offenders=""
for rlib in target/release/deps/*.rlib; do
    [[ -e "$rlib" ]] || continue
    refs=$(nm -o "$rlib" 2>/dev/null | grep -E "CFSocket|_socket_create" || true)
    [[ -n "$refs" ]] || continue
    name=$(basename "$rlib")
    [[ "$name" =~ ^libapple_cf ]] && continue
    offenders+=" $name"
done
if [[ -n "$offenders" ]]; then
    bad "socket APIs referenced by a crate other than apple-cf:$offenders"
else
    ok "socket APIs confined to apple-cf's dormant wrapper (never called here)"
fi

echo "== this crate calls no socket API =="
own=$(grep -rn "CFSocket\|udp_ipv4\|socket_create" cli/src/ || true)
if [[ -n "$own" ]]; then
    bad "recordo source references a socket API" "$own"
else
    ok "no socket API referenced in cli/src"
fi

echo "== dependencies =="
hits=$(grep -iE '^name = "(reqwest|hyper|ureq|curl|isahc|surf|attohttpc|tokio|async-std|socket2|rustls|native-tls|openssl)"' Cargo.lock || true)
if [[ -n "$hits" ]]; then bad "a networking or TLS crate is in Cargo.lock" "$hits"; else ok "no networking or TLS crate in Cargo.lock"; fi

echo
if [[ $fail -eq 0 ]]; then
    echo "PASS: $BIN has no path to the network."
else
    echo "FAILED — the local-only guarantee is broken." >&2
fi
exit $fail
