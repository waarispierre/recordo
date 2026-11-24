package main

import (
	"strings"
	"testing"
)

// sandboxHome points HOME (and so the config and recordings folders) at a temp dir.
func sandboxHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

func TestOpposingFlagsAreRejected(t *testing.T) {
	sandboxHome(t)
	root := rootCommand()
	root.SetArgs([]string{"windows", "--webcam", "--no-webcam"})
	root.SetOut(new(strings.Builder))
	root.SetErr(new(strings.Builder))
	if err := root.Execute(); err == nil {
		t.Fatal("--webcam with --no-webcam should be rejected")
	}
}
