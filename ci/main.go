// Command ci runs recordo's continuous integration, locally or on a runner.
//
// The point of this existing at all is that `go run ./ci` and a GitHub run execute the
// same list of checks, so a green run in one place means the same thing in the other.
//
// Checks are split by what they require, not by preference. recordo compiles only on
// macOS — it links ScreenCaptureKit, builds a Swift shim and renders through Metal — so
// anything that needs a build is macOS-only. The rest (formatting, licence policy,
// dependency bans, shell linting) reads source and metadata, and runs anywhere.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

type check struct {
	name string
	argv []string
	// tool is the binary that must exist for this check to mean anything. When it is
	// missing the check is skipped locally, but fails on CI — a pipeline that silently
	// skips half its checks is worse than one that fails.
	tool string
	// install is shown when tool is missing.
	install string
}

// portable checks read source and metadata; they need no build, so they run on any OS.
var portable = []check{
	{
		name: "format",
		argv: []string{"cargo", "fmt", "--all", "--check"},
		tool: "cargo",
	},
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
}

// native checks need a working build, so they need macOS.
var native = []check{
	{
		name: "clippy",
		argv: []string{"cargo", "clippy", "--all-targets", "--", "-D", "warnings"},
		tool: "cargo",
	},
	{name: "test", argv: []string{"cargo", "test"}, tool: "cargo"},
	{name: "build", argv: []string{"cargo", "build", "--release"}, tool: "cargo"},
	{name: "no-network-binary", argv: []string{"./scripts/check-local-only.sh"}},
}

func main() {
	var only string
	flag.StringVar(&only, "only", "all", "which half to run: all, portable, native")
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
		if err := runChecks(portable, ""); err != nil {
			return err
		}
		// Guards recordo's central promise at the dependency level. The binary is
		// checked separately by scripts/check-local-only.sh, but that needs macOS; this
		// catches a networking crate the moment it enters Cargo.lock, anywhere.
		fmt.Printf("  \x1b[36m▸\x1b[0m no-network-deps\n")
		if err := checkNoNetworkDeps(); err != nil {
			return err
		}
	}

	if only == "all" || only == "native" {
		if runtime.GOOS != "darwin" {
			fmt.Printf("\n  \x1b[33m!\x1b[0m skipping native checks: recordo builds only on macOS, this is %s\n", runtime.GOOS)
		} else if err := runChecks(native, "native"); err != nil {
			return err
		}
	}

	fmt.Printf("\n  \x1b[32m✓\x1b[0m all checks passed in %s\n\n", time.Since(start).Round(time.Second))
	return nil
}

func runChecks(checks []check, tag string) error {
	suffix := ""
	if tag != "" {
		suffix = fmt.Sprintf(" \x1b[90m(%s)\x1b[0m", tag)
	}
	for _, c := range checks {
		if c.tool != "" && !onPath(c.tool) {
			if onCI() {
				return fmt.Errorf("%s: %s is not installed (%s)", c.name, c.tool, c.install)
			}
			fmt.Printf("  \x1b[33m–\x1b[0m %s \x1b[90mskipped: %s not installed", c.name, c.tool)
			if c.install != "" {
				fmt.Printf(" — %s", c.install)
			}
			fmt.Print("\x1b[0m\n")
			continue
		}

		fmt.Printf("  \x1b[36m▸\x1b[0m %s%s\n", c.name, suffix)
		cmd := exec.Command(c.argv[0], c.argv[1:]...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				return fmt.Errorf("%s failed with exit code %d", c.name, exitErr.ExitCode())
			}
			return fmt.Errorf("%s: %w", c.name, err)
		}
	}
	return nil
}

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
	for _, line := range strings.Split(string(lock), "\n") {
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
func onCI() bool {
	return os.Getenv("CI") != ""
}
