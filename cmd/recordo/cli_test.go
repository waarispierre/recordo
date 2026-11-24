package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/waarispierre/recordo/internal/config"
)

// sandboxHome points HOME (and so the config and recordings folders) at a temp dir.
func sandboxHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

func TestConfigSetRollsBackAnInvalidValue(t *testing.T) {
	sandboxHome(t)
	_, path, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	if _, _, err := setSetting(path, "camera.zoom_percent", "not-a-number"); err == nil {
		t.Fatal("a string for a number should be rejected")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("the file should be restored byte for byte after a rejected value")
	}
}

func TestConfigSetAcceptsHexForColours(t *testing.T) {
	sandboxHome(t)
	_, path, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if _, v, err := setSetting(path, "style.bg_top", "#FF0000"); err != nil || !strings.HasPrefix(v, "[1") {
		t.Fatalf("got %q, %v", v, err)
	}
	cfg, err := config.LoadOrCreate(path)
	if err != nil || cfg.Style.BgTop[0] != 1 || cfg.Style.BgTop[1] != 0 {
		t.Fatalf("bg_top = %v, %v", cfg.Style.BgTop, err)
	}
	if _, _, err := setSetting(path, "style.bg_top", "chartreuse-ish"); err == nil {
		t.Fatal("a non-colour should be rejected")
	}
}

func TestConfigGetAndReset(t *testing.T) {
	sandboxHome(t)
	_, path, _ := loadConfig()
	if _, _, err := setSetting(path, "camera.zoom_percent", "30"); err != nil {
		t.Fatal(err)
	}
	if err := resetConfig(); err != nil {
		t.Fatal(err)
	}
	cfg, _ := config.LoadOrCreate(path)
	if cfg.Camera.ZoomPercent == 30 {
		t.Fatal("reset should restore the default zoom")
	}
	if err := configGet("camera.nope"); err == nil {
		t.Fatal("an unknown key should be an error")
	}
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
	if exists(filepath.Join(rendered, "capture.mp4")) || !exists(filepath.Join(rendered, "export.mp4")) {
		t.Error("rendered recording should lose its raw capture but keep the export")
	}
	if !exists(filepath.Join(rawOnly, "capture.mp4")) {
		t.Error("an unrendered recording is the only copy and must be left alone")
	}
}

func TestPruneAllRemovesWholeRecordings(t *testing.T) {
	home := sandboxHome(t)
	dir := recording(t, home, "2026-01-01_10-00-00", "capture.mp4", "export.mp4")
	if err := cmdPrune(false, 0, true, true); err != nil {
		t.Fatal(err)
	}
	if exists(dir) {
		t.Fatal("--all should remove the folder")
	}
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
