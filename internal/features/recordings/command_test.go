package recordings

import (
	"github.com/waarispierre/recordo/internal/cli"
	"os"
	"path/filepath"
	"testing"
)

// sandboxHome points HOME (and so the config and recordings folders) at a temp dir.
func sandboxHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

// recording makes a fake session folder with the given files.
func recording(t *testing.T, home, name string, files ...string) string {
	t.Helper()
	dir := filepath.Join(home, "Movies", "Recordo", name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestPruneNeverTouchesAnUnrenderedRecording(t *testing.T) {
	home := sandboxHome(t)
	rendered := recording(t, home, "2026-01-01_10-00-00", "capture.mp4", "frames.json", "export.mp4")
	rawOnly := recording(t, home, "2026-01-02_10-00-00", "capture.mp4", "frames.json")
	if err := cmdPrune(false, 0, false, true); err != nil {
		t.Fatal(err)
	}
	if cli.Exists(filepath.Join(rendered, "capture.mp4")) || !cli.Exists(filepath.Join(rendered, "export.mp4")) {
		t.Error("rendered recording should lose its raw capture but keep the export")
	}
	if !cli.Exists(filepath.Join(rawOnly, "capture.mp4")) {
		t.Error("an unrendered recording is the only copy and must be left alone")
	}
}

func TestPruneAllRemovesWholeRecordings(t *testing.T) {
	home := sandboxHome(t)
	dir := recording(t, home, "2026-01-01_10-00-00", "capture.mp4", "export.mp4")
	if err := cmdPrune(false, 0, true, true); err != nil {
		t.Fatal(err)
	}
	if cli.Exists(dir) {
		t.Fatal("--all should remove the folder")
	}
}
