// Command ci runs recordo's continuous integration, locally or on a runner.
//
// The pipeline is split in two because of a hard constraint: recordo only compiles on
// macOS. It links ScreenCaptureKit, compiles a Swift shim and renders through Metal,
// none of which exist in a Linux container. So:
//
//   - Portable checks (formatting, licences, dependency policy, shell linting) run in
//     containers through Dagger. They are hermetic and reproducible anywhere.
//   - Native checks (clippy, tests, release build, the no-network assertion) must run on
//     a macOS host, so they are executed directly.
//
// Running `go run ./ci` on a Mac therefore reproduces the whole of CI. On a Linux
// machine the portable half still runs; the native half is skipped with a clear notice
// rather than a confusing compile failure.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"dagger.io/dagger"
)

// rustVersion pins the toolchain so a container run and a CI run agree.
const rustVersion = "1.90"

type stage struct {
	name string
	run  func(context.Context, *dagger.Client) error
}

func main() {
	var only string
	var verbose bool
	flag.StringVar(&only, "only", "all", "which half to run: all, portable, native")
	flag.BoolVar(&verbose, "v", false, "show Dagger's full build log")
	flag.Parse()

	if err := run(context.Background(), only, verbose); err != nil {
		fmt.Fprintf(os.Stderr, "\n\x1b[31m✗\x1b[0m %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, only string, verbose bool) error {
	start := time.Now()
	fmt.Printf("\n  \x1b[1mrecordo ci\x1b[0m  \x1b[90m%s\x1b[0m\n\n", only)

	if only == "all" || only == "portable" {
		if err := runPortable(ctx, verbose); err != nil {
			return err
		}
	}
	if only == "all" || only == "native" {
		if err := runNative(ctx); err != nil {
			return err
		}
	}

	fmt.Printf("\n  \x1b[32m✓\x1b[0m all checks passed in %s\n\n", time.Since(start).Round(time.Second))
	return nil
}

// runPortable executes the container-safe checks through Dagger.
func runPortable(ctx context.Context, verbose bool) error {
	// Dagger's build log is long and mostly graph plumbing. Failures are surfaced by
	// sync() regardless, so the default is quiet and -v opts back in.
	logTo := io.Discard
	if verbose {
		logTo = os.Stderr
	}
	client, err := dagger.Connect(ctx, dagger.WithLogOutput(logTo))
	if err != nil {
		return fmt.Errorf("connect to dagger engine (is Docker running?): %w", err)
	}
	defer client.Close()

	stages := []stage{
		{"format", checkFormat},
		{"licences", checkLicences},
		{"shell", checkShell},
		{"no-network-deps", checkNoNetworkDeps},
	}
	for _, s := range stages {
		fmt.Printf("  \x1b[36m▸\x1b[0m %s\n", s.name)
		if err := s.run(ctx, client); err != nil {
			return fmt.Errorf("%s: %w", s.name, err)
		}
	}
	return nil
}

// source is the repository with build output and local settings excluded, so a dirty
// working tree cannot change the result.
func source(client *dagger.Client) *dagger.Directory {
	return client.Host().Directory(".", dagger.HostDirectoryOpts{
		Exclude: []string{"target/", ".git/", "out/", "recordo.toml", "ci/ci"},
	})
}

func rustBase(client *dagger.Client) *dagger.Container {
	return client.Container().
		From("rust:"+rustVersion+"-slim").
		// cargo-deny clones the RUSTSEC advisory database over git, and the slim image
		// ships neither git nor a CA bundle.
		WithExec([]string{"sh", "-c",
			"apt-get update -qq && apt-get install -y -qq --no-install-recommends git ca-certificates"}).
		WithMountedCache("/usr/local/cargo/registry", client.CacheVolume("cargo-registry")).
		WithMountedCache("/root/.cargo/advisory-dbs", client.CacheVolume("advisory-db")).
		WithDirectory("/src", source(client)).
		WithWorkdir("/src")
}

// sync runs a container and, on failure, surfaces what it actually printed.
//
// Dagger's default error is just the exit code, which turns a one-line diagnosis into
// guesswork.
func sync(ctx context.Context, c *dagger.Container) error {
	_, err := c.Sync(ctx)
	if err == nil {
		return nil
	}
	if out, e := c.Stdout(ctx); e == nil && strings.TrimSpace(out) != "" {
		fmt.Fprintf(os.Stderr, "\n%s\n", out)
	}
	if errOut, e := c.Stderr(ctx); e == nil && strings.TrimSpace(errOut) != "" {
		fmt.Fprintf(os.Stderr, "\n%s\n", errOut)
	}
	return err
}

func checkFormat(ctx context.Context, client *dagger.Client) error {
	// rustfmt parses source rather than compiling it, so it is happy on Linux even
	// though the crate itself is macOS-only.
	return sync(ctx, rustBase(client).
		WithExec([]string{"rustup", "component", "add", "rustfmt"}).
		WithExec([]string{"cargo", "fmt", "--all", "--check"}))
}

func checkLicences(ctx context.Context, client *dagger.Client) error {
	// cargo-deny reads Cargo.lock and crate metadata; it never builds the crate. This is
	// what keeps GPL code, and any direct FFmpeg linkage, out of the dependency graph.
	return sync(ctx, rustBase(client).
		WithExec([]string{"cargo", "install", "cargo-deny", "--locked"}).
		WithExec([]string{"cargo", "deny", "check"}))
}

func checkShell(ctx context.Context, client *dagger.Client) error {
	return sync(ctx, client.Container().
		From("koalaman/shellcheck-alpine:stable").
		WithDirectory("/src", source(client)).
		WithWorkdir("/src").
		WithExec([]string{"shellcheck", "scripts/check-local-only.sh"}))
}

// checkNoNetworkDeps guards recordo's central promise at the dependency level.
//
// scripts/check-local-only.sh asserts the same property against a built binary, but that
// needs macOS. This catches a networking crate the moment it enters Cargo.lock, on any
// machine.
func checkNoNetworkDeps(ctx context.Context, client *dagger.Client) error {
	const banned = `reqwest|hyper|ureq|curl|isahc|surf|attohttpc|tokio|async-std|socket2|rustls|native-tls|openssl`
	out, err := client.Container().
		From("alpine:3").
		WithDirectory("/src", source(client)).
		WithWorkdir("/src").
		// grep exits 1 when nothing matches, which is the outcome we want, so the result
		// is turned into text rather than an exit code.
		WithExec([]string{"sh", "-c",
			`grep -iE '^name = "(` + banned + `)"' Cargo.lock || true`}).
		Stdout(ctx)
	if err != nil {
		return err
	}
	if strings.TrimSpace(out) != "" {
		return fmt.Errorf("a networking crate entered Cargo.lock:\n%s", out)
	}
	return nil
}

// runNative executes the checks that genuinely need macOS.
func runNative(ctx context.Context) error {
	if runtime.GOOS != "darwin" {
		fmt.Printf("\n  \x1b[33m!\x1b[0m skipping native checks: recordo builds only on macOS, this is %s\n", runtime.GOOS)
		return nil
	}
	steps := []struct {
		name string
		argv []string
	}{
		{"clippy", []string{"cargo", "clippy", "--all-targets", "--", "-D", "warnings"}},
		{"test", []string{"cargo", "test"}},
		{"build", []string{"cargo", "build", "--release"}},
		{"no-network-binary", []string{"./scripts/check-local-only.sh"}},
	}
	for _, s := range steps {
		fmt.Printf("  \x1b[36m▸\x1b[0m %s \x1b[90m(native)\x1b[0m\n", s.name)
		cmd := exec.CommandContext(ctx, s.argv[0], s.argv[1:]...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				return fmt.Errorf("%s failed with exit code %d", s.name, exitErr.ExitCode())
			}
			return fmt.Errorf("%s: %w", s.name, err)
		}
	}
	return nil
}
