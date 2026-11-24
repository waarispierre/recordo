package settings_test

import (
	"os"
	"strings"
	"testing"

	"github.com/waarispierre/recordo/internal/domain/config"
	"github.com/waarispierre/recordo/internal/tui"
	tt "github.com/waarispierre/recordo/internal/tui/tuitest"
)

// selectSetting moves the settings list onto key from the top.
func selectSetting(h *tt.Harness, from, key string) {
	h.T.Helper()
	settings, err := config.Settings(h.ConfigPath)
	if err != nil {
		h.T.Fatal(err)
	}
	index := func(k string) int {
		for i, s := range settings {
			if s.Key == k {
				return i
			}
		}
		h.T.Fatalf("no setting %s", k)
		return 0
	}
	for range index(key) - index(from) {
		h.Press(tt.Char('j'))
	}
	h.WaitScreen("❯ " + key + " ")
}

func TestSettingsEditValidatesAndRollsBack(t *testing.T) {
	h := tt.Start(t, tui.Overrides{}, nil)
	read := func() string {
		b, err := os.ReadFile(h.ConfigPath)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	original := read()

	h.Press(tt.Char('2'))
	h.WaitScreen("what it does")
	selectSetting(h, "camera.zoom_percent", "camera.zoom_percent")
	h.Press(tt.Enter)
	h.WaitScreen("now 45.0")
	h.Type("lots")
	h.Press(tt.Enter)
	status := h.WaitStatusPrefix("rejected: ")
	if !strings.Contains(status, "— keeping 45.0") {
		t.Errorf("status %q does not say what was kept", status)
	}
	if read() != original {
		t.Fatal("an invalid value was left in the config")
	}

	// Escape and blank both leave it alone.
	h.Press(tt.Enter, tt.Esc)
	h.WaitStatus("cancelled")
	h.Press(tt.Enter, tt.Enter)
	h.WaitStatus("unchanged")

	h.Press(tt.Enter)
	h.Type("30")
	h.Press(tt.Enter)
	h.WaitStatus("camera.zoom_percent = 30")
	cfg, err := config.LoadOrCreate(h.ConfigPath)
	if err != nil || cfg.Camera.ZoomPercent != 30 {
		t.Fatalf("zoom = %v, %v", cfg.Camera.ZoomPercent, err)
	}
	h.WaitFor("the list to show the new value", func(m *tui.Model) bool {
		for _, line := range strings.Split(m.Screen(), "\n") {
			if strings.Contains(line, "camera.zoom_percent ") && strings.Contains(line, " 30 ") {
				return true
			}
		}
		return false
	})

	// Colour keys take hex.
	selectSetting(h, "camera.zoom_percent", "style.bg_top")
	h.Press(tt.Enter)
	h.WaitScreen("hex like #5C66C7, or r, g, b")
	h.Type("purple")
	h.Press(tt.Enter)
	h.WaitStatus("purple is not a colour — try #5C66C7")
	h.Press(tt.Enter)
	h.Type("#5C66C7")
	h.Press(tt.Enter)
	h.WaitStatus("style.bg_top = [0.361, 0.400, 0.780]")

	// A value that parses but fails the extra check is rolled back too.
	before := read()
	selectSetting(h, "style.bg_top", "style.background_image")
	h.Press(tt.Enter)
	h.Type("/no/such/image.png")
	h.Press(tt.Enter)
	if status := h.WaitStatusPrefix("rejected: "); !strings.Contains(status, "does not exist") {
		t.Errorf("status %q", status)
	}
	if read() != before {
		t.Fatal("a missing background image was left in the config")
	}
	h.Quit()
}

// A modal edit takes every key: q types a q rather than quitting, and 1 does not switch
// tabs.
func TestAnEditTakesEveryKey(t *testing.T) {
	h := tt.Start(t, tui.Overrides{}, nil)
	h.Press(tt.Char('2'), tt.Enter)
	h.WaitScreen("now ")
	h.Type("q1")
	h.WaitScreen("› q1")
	h.Press(tt.Esc)
	h.WaitStatus("cancelled")
	h.WaitScreen("what it does")
	h.Quit()
}
