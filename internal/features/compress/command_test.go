package compress

import (
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

func TestCompressFindsTheNewestRenderedRecording(t *testing.T) {
	home := sandboxHome(t)
	recording(t, home, "2026-01-01_10-00-00", "export.mp4")
	want := recording(t, home, "2026-01-02_10-00-00", "export.mp4")
	recording(t, home, "2026-01-03_10-00-00", "capture.mp4")
	if got, err := compressDir(""); err != nil || got != want {
		t.Fatalf("got %q, %v; want %q", got, err, want)
	}
}
