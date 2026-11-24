#!/bin/bash
# Asserts the built binary cannot reach the network.
#
# recordo's core promise is that recordings never leave the machine. That is a property of
# the dependency graph, which a single careless `go get` can break — so it is checked
# mechanically rather than assumed.
#
# Why Go symbols rather than socket imports: on macOS, Go's syscall package declares the
# libc socket functions (_socket, _connect, _getaddrinfo, …) in every Go binary, whether
# or not it ever networks, so their presence proves nothing. What does prove something is
# the Go code the linker kept: it drops everything unreachable, so if no dialer, listener,
# resolver, connection, HTTP or TLS code survives, there is no path to the network.
#
# One accepted import, verified rather than assumed: spf13/pflag (Cobra's flag parser)
# imports the standard `net` package to parse IP-address flags. recordo defines none, and
# the check below confirms that only net's inert parts (IP constants, init) are linked.
#
# Note on style: output is captured into variables before matching. Piping into `grep -q`
# under `set -o pipefail` makes the pipeline fail when grep short-circuits and the
# producer takes SIGPIPE — which once silently inverted a check here and reported PASS on
# a binary that should have failed.
set -euo pipefail

BIN="${1:-bin/recordo}"
if [[ ! -x "$BIN" ]]; then
    echo "usage: $0 [path-to-binary]   (build first: go build -o bin/recordo ./cmd/recordo)" >&2
    exit 2
fi

fail=0
ok()  { printf '  ok    %s\n' "$1"; }
bad() { printf '  FAIL  %s\n' "$1" >&2; if [[ -n "${2:-}" ]]; then printf '%s\n' "$2" | sed 's/^/          /' >&2; fi; fail=1; }

libs=$(otool -L "$BIN" || true)
undefined=$(nm -u "$BIN" 2>/dev/null || true)
gosyms=$(go tool nm "$BIN" 2>/dev/null | awk '{print $NF}' || true)

# Guard the inputs positively. A tool that failed, or printed something unexpected, would
# turn every check below into a no-op that always passes — so require what every real
# answer contains: every macOS binary links libSystem, and every Go binary has main.main.
if ! grep -q 'libSystem' <<<"$libs"; then
    bad "could not read the binary's linked libraries (otool) — nothing was checked"
fi
if ! grep -qx 'main.main' <<<"$gosyms"; then
    bad "could not read the binary's Go symbols (go tool nm) — nothing was checked"
fi

echo "== linked frameworks =="
hits=$(printf '%s\n' "$libs" | grep -iE "CFNetwork|/Network\.framework|Security\.framework|libcurl" || true)
if [[ -n "$hits" ]]; then bad "a networking framework is linked" "$hits"; else ok "no networking framework linked"; fi

echo "== URL loading =="
hits=$(printf '%s\n' "$undefined" | grep -E "NSURLSession|NSURLConnection|CFURLConnection|CFHTTP" || true)
if [[ -n "$hits" ]]; then bad "a URL-loading symbol is imported" "$hits"; else ok "no URL-loading symbols"; fi

echo "== Go networking code =="
hits=$(printf '%s\n' "$gosyms" | grep -E '^net\.(\(\*Dialer\)|Dial|Listen|\(\*Resolver\)|Lookup|\(\*sysDialer\)|\(\*conn\)|\(\*TCP|\(\*UDP|\(\*Unix)|^net/http\.|^crypto/tls\.|^golang\.org/x/net/' | head -20 || true)
if [[ -n "$hits" ]]; then
    bad "dialing, listening, resolving, HTTP or TLS code is linked" "$hits"
else
    ok "no dialer, listener, resolver, connection, HTTP or TLS code linked"
fi

echo "== net importers =="
importers=$(GOOS=darwin GOARCH=arm64 CGO_ENABLED=1 go list -deps -f '{{if not .Standard}}{{range .Imports}}{{if eq . "net"}}{{$.ImportPath}}{{"\n"}}{{end}}{{end}}{{end}}' ./cmd/recordo | grep -v '^$' || true)
unexpected=$(printf '%s\n' "$importers" | grep -v '^github.com/spf13/pflag$' | grep -v '^$' || true)
if [[ -n "$unexpected" ]]; then
    bad "a package other than pflag imports net" "$unexpected"
else
    ok "only pflag imports net (IP-address flag parsing; recordo defines no such flag)"
fi

echo
if [[ $fail -eq 0 ]]; then
    echo "PASS: $BIN has no path to the network."
else
    echo "FAILED — the local-only guarantee is broken." >&2
fi
exit $fail
