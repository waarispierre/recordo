package settings

import (
	"github.com/waarispierre/recordo/internal/cli"
	"github.com/waarispierre/recordo/internal/domain/config"
	"os"
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

func TestConfigSetRollsBackAnInvalidValue(t *testing.T) {
	sandboxHome(t)
	_, path, err := cli.LoadConfig()
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
	_, path, err := cli.LoadConfig()
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
	_, path, _ := cli.LoadConfig()
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
