// Package ffmpeg locates and runs the helper binaries recordo shells out to: ffmpeg and
// ffprobe.
//
// This process holds Screen Recording, Accessibility and Input Monitoring grants. A
// binary planted in an earlier $PATH entry would inherit all three, so helpers are
// resolved against known system locations first and $PATH is only a last resort.
package ffmpeg

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// trustedDirs are searched before $PATH, in order.
var trustedDirs = []string{
	"/opt/homebrew/bin",
	"/usr/local/bin",
	"/opt/local/bin",
	"/usr/bin",
	"/bin",
}

// Find returns the absolute path to name, preferring trusted directories over $PATH.
func Find(name string) (string, bool) {
	for _, dir := range trustedDirs {
		if p := filepath.Join(dir, name); isExecutable(p) {
			return p, true
		}
	}
	// Fall back to $PATH so unusual installs still work, accepting the weaker guarantee.
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if p := filepath.Join(dir, name); isExecutable(p) {
			return p, true
		}
	}
	return "", false
}

// Require is Find, as an error when the binary is missing.
func Require(name string) (string, error) {
	p, ok := Find(name)
	if !ok {
		return "", fmt.Errorf("%s not found — try `brew install ffmpeg`", name)
	}
	return p, nil
}

func isExecutable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0
}

// CheckNotOptionLike rejects paths that ffmpeg would read as flags.
//
// ffmpeg has no `--` terminator, so an argument beginning with '-' is parsed as an option
// rather than a filename.
func CheckNotOptionLike(label, value string) error {
	if strings.HasPrefix(value, "-") {
		return fmt.Errorf("%s %q starts with '-', which ffmpeg would read as an option", label, value)
	}
	return nil
}
