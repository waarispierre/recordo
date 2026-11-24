// Command ci runs recordo's checks, locally and on GitHub.
//
// It exists so that `go run ci/main.go` and a CI run execute the same list, which means
// a green run in one place means the same thing in the other. Written in Go rather than
// shell for two reasons that earn their keep: the independent checks run concurrently
// with their output kept separate, and a missing tool is handled differently depending
// on whether a human or a runner is watching.
//
// Checks are grouped by what they require, not by preference. recordo compiles only on
// macOS — it calls ScreenCaptureKit and AVFoundation through cgo and renders through
// Metal — so anything needing a build is macOS-only. The rest reads source and metadata
// and runs anywhere.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
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

// portable checks read source and metadata. They build nothing that needs macOS, so they
// run anywhere, and concurrently.
var portable = []check{
	{name: "format", fn: checkFormat, tool: "gofmt"},
	{name: "modules", argv: []string{"go", "mod", "verify"}, tool: "go"},
	{name: "licences", fn: checkLicences, tool: "go"},
	{
		name:    "shell",
		argv:    []string{"shellcheck", "install.sh", "scripts/check-local-only.sh"},
		tool:    "shellcheck",
		install: "brew install shellcheck",
	},
	{name: "no-network-deps", fn: checkNoNetworkDeps, tool: "go"},
}

// native checks need a working build — cgo against ScreenCaptureKit, AVFoundation and
// Metal — so macOS, one at a time: each builds on the one before.
var native = []check{
	{name: "vet", argv: []string{"go", "vet", "./..."}, tool: "go"},
	{name: "test", argv: []string{"go", "test", "-race", "./..."}, tool: "go"},
	{name: "build", argv: []string{"go", "build", "-trimpath", "-o", "bin/recordo", "./cmd/recordo"}, tool: "go"},
	{name: "no-network-binary", argv: []string{"./scripts/check-local-only.sh", "bin/recordo"}},
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

// checkFormat fails when any Go file is not gofmt-clean.
func checkFormat() error {
	out, err := exec.Command("gofmt", "-l", "cmd", "internal", "ci").CombinedOutput()
	if err != nil {
		return fmt.Errorf("gofmt: %v\n%s", err, out)
	}
	if files := strings.TrimSpace(string(out)); files != "" {
		return fmt.Errorf("not gofmt-clean (run gofmt -w):\n%s", files)
	}
	return nil
}

// listDeps runs `go list` over the darwin build of every package, which is the only
// build there is.
func listDeps(format string) ([]string, error) {
	cmd := exec.Command("go", "list", "-deps", "-f", format, "./...")
	cmd.Env = append(os.Environ(), "GOOS=darwin", "GOARCH=arm64", "CGO_ENABLED=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("go list: %v\n%s", err, out)
	}
	return strings.Split(strings.TrimSpace(string(out)), "\n"), nil
}

// netImporters may import the standard "net" package. pflag, Cobra's flag parser, imports
// it to parse IP-address flags; recordo defines none, and the linker drops the dialing
// and resolving code — scripts/check-local-only.sh verifies that against the binary.
var netImporters = map[string]bool{"github.com/spf13/pflag": true}

// checkNoNetworkDeps guards recordo's central promise — recordings never leave the
// machine — at the dependency level. scripts/check-local-only.sh asserts the same
// property against a built binary, but that needs macOS; this catches a networking
// package the moment it enters the graph, anywhere.
func checkNoNetworkDeps() error {
	lines, err := listDeps(`{{.ImportPath}}|{{.Standard}}|{{join .Imports ","}}`)
	if err != nil {
		return err
	}
	banned := []string{"net/http", "crypto/tls", "net/rpc", "net/smtp", "golang.org/x/net"}
	var found []string
	for _, line := range lines {
		parts := strings.SplitN(line, "|", 3)
		if len(parts) != 3 {
			continue
		}
		pkg, std, imports := parts[0], parts[1] == "true", strings.Split(parts[2], ",")
		for _, b := range banned {
			if pkg == b || strings.HasPrefix(pkg, b+"/") {
				found = append(found, pkg)
			}
		}
		if !std && !netImporters[pkg] && slices.Contains(imports, "net") {
			found = append(found, pkg+" imports net")
		}
	}
	if len(found) > 0 {
		return fmt.Errorf("networking entered the dependency graph: %s", strings.Join(found, ", "))
	}
	return nil
}

// allowedLicences is the licence policy: permissive
// licences only. Above all no GPL — libx264/libx265 are why ffmpeg is run as a separate
// program rather than linked.
var allowedLicences = []string{
	"MIT License", "Permission is hereby granted, free of charge", // MIT
	"Apache License", // Apache-2.0
	"Redistribution and use in source and binary forms",                      // BSD-2/3-Clause
	"Permission to use, copy, modify, and/or distribute",                     // ISC, 0BSD
	"This is free and unencumbered software released into the public domain", // Unlicense
	"Mozilla Public License",                                                 // MPL-2.0
}

var forbiddenLicences = []string{"GNU GENERAL PUBLIC LICENSE", "GNU AFFERO GENERAL PUBLIC LICENSE", "GNU LESSER GENERAL PUBLIC LICENSE"}

// checkLicences reads the licence of every module compiled into recordo — the ones its
// licence obligations actually come from. A module with no recognisable permissive
// licence fails, as does any GPL-family licence anywhere in it. It reads modules from
// `go list -deps` rather than `go mod download all`, which would also fetch test-only
// modules of dependencies and leave go.sum modified after a local run.
func checkLicences() error {
	lines, err := listDeps(`{{with .Module}}{{if not .Main}}{{.Path}}|{{.Dir}}{{end}}{{end}}`)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	var problems []string
	for _, line := range lines {
		path, dir, ok := strings.Cut(line, "|")
		if !ok || seen[path] {
			continue
		}
		seen[path] = true
		if dir == "" {
			problems = append(problems, path+": not downloaded (run go mod download)")
			continue
		}
		text, err := licenceText(dir)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", path, err))
			continue
		}
		upper := strings.ToUpper(text)
		for _, f := range forbiddenLicences {
			if strings.Contains(upper, f) {
				problems = append(problems, path+": "+strings.ToLower(f))
			}
		}
		if !slices.ContainsFunc(allowedLicences, func(a string) bool { return strings.Contains(text, a) }) {
			problems = append(problems, path+": licence not on the allow-list")
		}
	}
	if len(seen) == 0 {
		return errors.New("found no modules to check — the check inspected nothing")
	}
	if len(problems) > 0 {
		return fmt.Errorf("licence policy:\n  %s", strings.Join(problems, "\n  "))
	}
	return nil
}

func licenceText(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, e := range entries {
		name := strings.ToUpper(e.Name())
		if !e.IsDir() && (strings.HasPrefix(name, "LICENSE") || strings.HasPrefix(name, "LICENCE") || strings.HasPrefix(name, "COPYING")) {
			data, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				return "", err
			}
			b.Write(data)
		}
	}
	if b.Len() == 0 {
		return "", errors.New("no licence file")
	}
	return b.String(), nil
}

func onPath(bin string) bool {
	_, err := exec.LookPath(bin)
	return err == nil
}

// onCI reports whether this is an automated run, where a skipped check is a failure.
func onCI() bool { return os.Getenv("CI") != "" }
