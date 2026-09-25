// Command ci runs recordo's checks, locally and on GitHub.
//
// It exists so that `go run ci/main.go` and a CI run execute the same list, which means
// a green run in one place means the same thing in the other. Written in Go rather than
// shell for two reasons that earn their keep: the independent checks run concurrently
// with their output kept separate, and a missing tool is handled differently depending
// on whether a human or a runner is watching.
//
// Checks are grouped by what they require, not by preference. recordo compiles only on
// macOS — it links ScreenCaptureKit, builds a Swift shim and renders through Metal — so
// anything needing a build is macOS-only. The rest reads source and metadata and runs
// anywhere.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

type check struct {
	name string
	argv []string
	// fn replaces argv for checks implemented in Go rather than shelled out.
	fn func() error
	// tool must exist for this check to mean anything. When missing it is skipped
	// locally but fails on CI: a pipeline that quietly skips half its checks is worse
	// than one that fails.
	tool    string
	install string
}

// portable checks read source and metadata. No build, so any OS, and they do not
// contend for cargo's target-directory lock — which is what makes running them
// concurrently safe.
var portable = []check{
	{name: "format", argv: []string{"cargo", "fmt", "--all", "--check"}, tool: "cargo"},
	{
		name:    "licences",
		argv:    []string{"cargo", "deny", "check"},
		tool:    "cargo-deny",
		install: "cargo install cargo-deny --locked",
	},
	{
		name:    "shell",
		argv:    []string{"shellcheck", "scripts/check-local-only.sh"},
		tool:    "shellcheck",
		install: "brew install shellcheck",
	},
	{name: "no-network-deps", fn: checkNoNetworkDeps},
}

// native checks need a working build, so macOS — and they run one at a time, because
// cargo locks the target directory.
var native = []check{
	{name: "clippy", argv: []string{"cargo", "clippy", "--all-targets", "--", "-D", "warnings"}, tool: "cargo"},
	{name: "test", argv: []string{"cargo", "test"}, tool: "cargo"},
	{name: "build", argv: []string{"cargo", "build", "--release"}, tool: "cargo"},
	{name: "no-network-binary", argv: []string{"./scripts/check-local-only.sh"}},
}

type result struct {
	check   check
	skipped bool
	output  []byte
	err     error
	took    time.Duration
}

func main() {
	var only string
	flag.StringVar(&only, "only", "all", "which group to run: all, portable, native")
	flag.Parse()

	if err := run(only); err != nil {
		fmt.Fprintf(os.Stderr, "\n\x1b[31m✗\x1b[0m %v\n\n", err)
		os.Exit(1)
	}
}

func run(only string) error {
	start := time.Now()
	fmt.Printf("\n  \x1b[1mrecordo ci\x1b[0m  \x1b[90m%s\x1b[0m\n\n", only)

	if only == "all" || only == "portable" {
		if err := report(runConcurrently(portable), ""); err != nil {
			return err
		}
	}
	if only == "all" || only == "native" {
		switch {
		case runtime.GOOS != "darwin":
			fmt.Printf("  \x1b[33m–\x1b[0m native checks \x1b[90mskipped: recordo builds only on macOS, this is %s\x1b[0m\n", runtime.GOOS)
		default:
			if err := report(runSequentially(native), "native"); err != nil {
				return err
			}
		}
	}

	fmt.Printf("\n  \x1b[32m✓\x1b[0m all checks passed in %s\n\n", time.Since(start).Round(time.Millisecond*100))
	return nil
}

// runConcurrently runs checks in parallel, buffering each one's output so parallel
// writers cannot interleave into unreadable noise.
func runConcurrently(checks []check) []result {
	results := make([]result, len(checks))
	var wg sync.WaitGroup
	for i, c := range checks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = execute(c)
		}()
	}
	wg.Wait()
	return results
}

func runSequentially(checks []check) []result {
	results := make([]result, 0, len(checks))
	for _, c := range checks {
		r := execute(c)
		results = append(results, r)
		// Stop at the first failure: later steps build on earlier ones, so their output
		// would be noise.
		if r.err != nil {
			break
		}
	}
	return results
}

func execute(c check) result {
	if c.tool != "" && !onPath(c.tool) {
		if !onCI() {
			return result{check: c, skipped: true}
		}
		return result{check: c, err: fmt.Errorf("%s is not installed (%s)", c.tool, c.install)}
	}

	start := time.Now()
	if c.fn != nil {
		return result{check: c, err: c.fn(), took: time.Since(start)}
	}

	cmd := exec.Command(c.argv[0], c.argv[1:]...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		err = fmt.Errorf("exit code %d", exitErr.ExitCode())
	}
	return result{check: c, output: buf.Bytes(), err: err, took: time.Since(start)}
}

// report prints each result in the order the checks were declared, so concurrent
// execution does not produce a different-looking log each run.
func report(results []result, tag string) error {
	suffix := ""
	if tag != "" {
		suffix = fmt.Sprintf(" \x1b[90m(%s)\x1b[0m", tag)
	}
	var failed error
	for _, r := range results {
		switch {
		case r.skipped:
			fmt.Printf("  \x1b[33m–\x1b[0m %s \x1b[90mskipped: %s not installed", r.check.name, r.check.tool)
			if r.check.install != "" {
				fmt.Printf(" — %s", r.check.install)
			}
			fmt.Print("\x1b[0m\n")
		case r.err != nil:
			fmt.Printf("  \x1b[31m✗\x1b[0m %s%s \x1b[90m%s\x1b[0m\n", r.check.name, suffix, r.took.Round(time.Millisecond))
			if len(r.output) > 0 {
				fmt.Printf("\n%s\n", r.output)
			}
			if failed == nil {
				failed = fmt.Errorf("%s: %w", r.check.name, r.err)
			}
		default:
			fmt.Printf("  \x1b[32m✓\x1b[0m %s%s \x1b[90m%s\x1b[0m\n", r.check.name, suffix, r.took.Round(time.Millisecond))
		}
	}
	return failed
}

// checkNoNetworkDeps guards recordo's central promise at the dependency level.
//
// scripts/check-local-only.sh asserts the same property against a built binary, but that
// needs macOS. This catches a networking crate the moment it enters Cargo.lock, anywhere.
func checkNoNetworkDeps() error {
	lock, err := os.ReadFile("Cargo.lock")
	if err != nil {
		return fmt.Errorf("read Cargo.lock: %w", err)
	}
	banned := []string{
		"reqwest", "hyper", "ureq", "curl", "isahc", "surf", "attohttpc",
		"tokio", "async-std", "socket2", "rustls", "native-tls", "openssl",
	}
	var found []string
	for line := range strings.Lines(string(lock)) {
		name, ok := strings.CutPrefix(strings.TrimSpace(line), "name = ")
		if !ok {
			continue
		}
		name = strings.Trim(name, `"`)
		for _, b := range banned {
			if strings.EqualFold(name, b) {
				found = append(found, name)
			}
		}
	}
	if len(found) > 0 {
		return fmt.Errorf("networking crates entered Cargo.lock: %s", strings.Join(found, ", "))
	}
	return nil
}

func onPath(bin string) bool {
	_, err := exec.LookPath(bin)
	return err == nil
}

// onCI reports whether this is an automated run, where a skipped check is a failure.
func onCI() bool { return os.Getenv("CI") != "" }
