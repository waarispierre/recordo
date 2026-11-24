package config

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMissingSettingsAreAddedWithTheirComments(t *testing.T) {
	// A config as an older version would have written it: valid, but predating
	// follow_deadzone_percent.
	old := "[camera]\nzoom_percent = 30.0\n\n[style]\npadding = 48.0\n"
	migrated, changed, err := addMissingSettings(old)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("should need migrating")
	}
	if !strings.Contains(migrated, "follow_deadzone_percent") {
		t.Error("follow_deadzone_percent not added")
	}
	// The explanation travels with the key, so a migrated file reads like a fresh one.
	if !strings.Contains(migrated, "How far the cursor may wander") {
		t.Error("comment not carried across")
	}
	// An existing value is never overwritten by the default.
	if !strings.Contains(migrated, "zoom_percent = 30.0") {
		t.Error("existing value overwritten")
	}
	// And the result is still loadable.
	cfg, err := Parse(migrated)
	if err != nil {
		t.Fatalf("migrated file does not parse: %v\n%s", err, migrated)
	}
	if cfg.Camera.ZoomPercent != 30.0 {
		t.Errorf("zoom_percent = %v, want 30", cfg.Camera.ZoomPercent)
	}
}

func TestACompleteConfigIsLeftAlone(t *testing.T) {
	// No rewrite, so a file the user has formatted by hand is not churned.
	_, changed, err := addMissingSettings(DefaultTOML)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("complete config was rewritten")
	}
}

func TestAMissingSectionIsCreated(t *testing.T) {
	onlyCamera := "[camera]\nzoom_percent = 45.0\n"
	migrated, _, err := addMissingSettings(onlyCamera)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"[style]",
		// A whole new section arrives with its own heading comment, not just its keys.
		"[audio]",
		"Record your voice while you record the screen",
		"[webcam]",
	} {
		if !strings.Contains(migrated, want) {
			t.Errorf("migrated file lacks %q", want)
		}
	}
	if _, err := Parse(migrated); err != nil {
		t.Errorf("migrated file does not parse: %v", err)
	}
}

func TestMigrationPlacesKeysLikeTomlEdit(t *testing.T) {
	// Missing keys follow the section's last key, ahead of comments that belong to the
	// next header; a missing section goes after the last statement, ahead of trailing
	// comments. A missing final newline is supplied.
	old := "[style]\npadding = 10.0\n\n# about camera\n[camera]\nzoom_percent = 30.0\n# trailing"
	migrated, _, err := addMissingSettings(old)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(migrated, "[style]\npadding = 10.0\n# Roughly matches macOS") {
		t.Errorf("style keys not inserted after padding:\n%s", migrated)
	}
	if !strings.Contains(migrated, "corner_radius = 13.0\nshadow_offset") {
		t.Errorf("style block not contiguous:\n%s", migrated)
	}
	if !strings.Contains(migrated, "browser_crop_top = 88.0\n\n# about camera\n[camera]\nzoom_percent = 30.0\n# Seconds to ramp in") {
		t.Errorf("camera keys misplaced:\n%s", migrated)
	}
	if !strings.HasSuffix(migrated, "preview_s = 3.0\n# trailing") {
		t.Errorf("new sections should precede trailing comments:\n%s", migrated)
	}
	if _, err := Parse(migrated); err != nil {
		t.Errorf("migrated file does not parse: %v", err)
	}
	if again, changed, _ := addMissingSettings(migrated); changed {
		t.Errorf("second migration changed the file again:\n%s", again)
	}
}

func TestDefaultFileMatchesDefaults(t *testing.T) {
	got, err := Parse(DefaultTOML)
	if err != nil {
		t.Fatal(err)
	}
	want := Default()
	// Default() derives zoom_percent from the camera model's max_zoom, (1.45-1)*100,
	// which is 44.99999999999999 in floating point; the file says 45.0. Same in Rust.
	if math.Abs(got.Camera.ZoomPercent-want.Camera.ZoomPercent) > 1e-9 {
		t.Errorf("zoom_percent: file %v, default %v", got.Camera.ZoomPercent, want.Camera.ZoomPercent)
	}
	got.Camera.ZoomPercent = want.Camera.ZoomPercent
	if got != want {
		t.Errorf("DefaultTOML disagrees with Default():\n file    %+v\n default %+v", got, want)
	}
}

func TestParse(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		check   func(Config) bool
		wantErr string
	}{
		{name: "empty file is all defaults", text: "", check: func(c Config) bool { return c == Default() }},
		{name: "integer into float field", text: "[camera]\nzoom_percent = 30\n", check: func(c Config) bool { return c.Camera.ZoomPercent == 30 }},
		{name: "integer array into float array", text: "[webcam]\noffset = [1, -2]\n", check: func(c Config) bool { return c.Webcam.Offset == [2]float32{1, -2} }},
		{name: "multi-line array with comments", text: "[style]\nbg_top = [\n  0.1, # red\n  0.2,\n  0.3,\n]\n", check: func(c Config) bool { return c.Style.BgTop == [3]float32{0.1, 0.2, 0.3} }},
		{name: "literal string", text: "[style]\nbackground_image = '~/a\\b.png'\n", check: func(c Config) bool { return c.Style.BackgroundImage == `~/a\b.png` }},
		{name: "escaped basic string", text: "[audio]\ndevice = \"Mic \\\"Pro\\\" \\u00e9\"\n", check: func(c Config) bool { return c.Audio.Device == `Mic "Pro" é` }},
		{name: "multi-line basic string", text: "[audio]\ndevice = \"\"\"\nab \\\n   cd\"\"\"\n", check: func(c Config) bool { return c.Audio.Device == "ab cd" }},
		{name: "quoted key and header", text: "[\"camera\"]\n\"hold_s\" = 2.0\n", check: func(c Config) bool { return c.Camera.HoldS == 2 }},
		{name: "underscored and exponent numbers", text: "[camera]\nzoom_percent = 1_0\nhold_s = 1e1\n", check: func(c Config) bool { return c.Camera.ZoomPercent == 10 && c.Camera.HoldS == 10 }},
		{name: "crlf line endings", text: "[audio]\r\nmicrophone = true\r\n", check: func(c Config) bool { return c.Audio.Microphone }},
		{name: "nan is accepted", text: "[camera]\nhold_s = nan\n", check: func(c Config) bool { return math.IsNaN(c.Camera.HoldS) }},
		{name: "unknown key", text: "[camera]\nzoom = 1.0\n", wantErr: "unknown setting camera.zoom"},
		{name: "unknown section", text: "[cameras]\n", wantErr: "unknown section [cameras]"},
		{name: "root key", text: "zoom = 1.0\n", wantErr: "unknown setting"},
		{name: "wrong type", text: "[audio]\nmicrophone = \"yes\"\n", wantErr: "expected true or false"},
		{name: "wrong array length", text: "[style]\nbg_top = [0.1, 0.2]\n", wantErr: "expected an array of 3"},
		{name: "duplicate key", text: "[camera]\nhold_s = 1.0\nhold_s = 2.0\n", wantErr: "duplicate key"},
		{name: "duplicate table", text: "[camera]\n[camera]\n", wantErr: "duplicate table"},
		{name: "leading zero", text: "[camera]\nhold_s = 01\n", wantErr: "invalid value"},
		{name: "unterminated string", text: "[audio]\ndevice = \"abc\n", wantErr: "unterminated string"},
		{name: "two values on a line", text: "[camera]\nhold_s = 1.0 2.0\n", wantErr: "expected end of line"},
		{name: "datetime", text: "[camera]\nhold_s = 1979-05-27\n", wantErr: "dates and times"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Parse(tt.text)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !tt.check(cfg) {
				t.Errorf("unexpected config: %+v", cfg)
			}
		})
	}
}

func writeTemp(t *testing.T, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestLoadOrCreateWritesTheCommentedDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	cfg, err := LoadOrCreate(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg != Default() {
		t.Error("fresh config is not the default")
	}
	if got := readFile(t, path); got != DefaultTOML {
		t.Error("written file is not DefaultTOML")
	}
}

func TestLoadOrCreateMigratesOnDisk(t *testing.T) {
	path := writeTemp(t, "[camera]\nzoom_percent = 30.0\n")
	cfg, err := LoadOrCreate(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Camera.ZoomPercent != 30 {
		t.Errorf("zoom_percent = %v", cfg.Camera.ZoomPercent)
	}
	if !strings.Contains(readFile(t, path), "[webcam]") {
		t.Error("file was not migrated in place")
	}
}

func TestSetValuePreservesCommentsAndLayout(t *testing.T) {
	path := writeTemp(t, DefaultTOML)
	if err := SetValue(path, "camera.zoom_percent", "30"); err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(DefaultTOML, "zoom_percent = 45.0", "zoom_percent = 30", 1)
	if got := readFile(t, path); got != want {
		t.Errorf("unexpected rewrite:\n%s", got)
	}

	// Only the value's bytes change: odd spacing and a trailing comment survive.
	path = writeTemp(t, "# top\n[style]\npadding=1.0   # mine\nchrome = 'auto'\n")
	if err := SetValue(path, "style.padding", "2.5"); err != nil {
		t.Fatal(err)
	}
	if err := SetValue(path, "style.chrome", "window"); err != nil {
		t.Fatal(err)
	}
	if got, want := readFile(t, path), "# top\n[style]\npadding=2.5   # mine\nchrome = \"window\"\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSetValueErrors(t *testing.T) {
	path := writeTemp(t, DefaultTOML)
	tests := []struct{ key, want string }{
		{"zoom_percent", "expected section.key"},
		{"cameras.zoom_percent", "no such section: cameras"},
		{".zoom_percent", "no such section"},
		{"camera.zoom", "no such setting: camera.zoom"},
	}
	for _, tt := range tests {
		err := SetValue(path, tt.key, "1")
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("SetValue(%q) err = %v, want %q", tt.key, err, tt.want)
		}
	}
	if readFile(t, path) != DefaultTOML {
		t.Error("a failed set modified the file")
	}
}

func TestInferValue(t *testing.T) {
	tests := []struct{ in, want string }{
		{"true", "true"},
		{" false ", "false"},
		{"True", `"True"`}, // only exact booleans, as Rust's bool parser
		{"30", "30"},
		{"+7", "7"},
		{"007", "7"},
		{"45.0", "45.0"},
		{"45.", "45.0"},
		{".5", "0.5"},
		{"0.1", "0.1"},
		{"1e3", "1000.0"},
		{"1e400", "inf"},
		{"-0.0", "-0.0"},
		{"inf", "inf"},
		{"0x10", `"0x10"`}, // Rust does not parse hex, so it stays text
		{"1_000", `"1_000"`},
		{"[0.1, 0.2, 0.3]", "[0.1, 0.2, 0.3]"},
		{"[1,2]", "[1, 2]"},
		{"[]", "[]"},
		{`["a", b]`, `["a", "b"]`},
		{"[true]", `["true"]`}, // array elements are never booleans in the Rust version
		{`"bottom-left"`, `"bottom-left"`},
		{"rect", `"rect"`},
		{`C:\path`, `'C:\path'`},
		{`it's "x"`, `"it's \"x"`}, // surrounding quotes are trimmed, as in Rust
		{"", `""`},
	}
	for _, tt := range tests {
		if got := inferValue(tt.in); got != tt.want {
			t.Errorf("inferValue(%q) = %s, want %s", tt.in, got, tt.want)
		}
		// Whatever comes out must be valid TOML.
		if _, err := parseDocument("k = " + inferValue(tt.in) + "\n"); err != nil {
			t.Errorf("inferValue(%q) produced invalid TOML: %v", tt.in, err)
		}
	}
}

func TestFlatten(t *testing.T) {
	path := writeTemp(t, "[camera]\nzoom_percent = 30   # note\n[style]\nbg_top = [ 0.1,0.2, 0.3 ]\n")
	got, err := Flatten(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []Entry{
		{"camera.zoom_percent", "30"},
		{"style.bg_top", "[ 0.1,0.2, 0.3 ]"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d = %v, want %v", i, got[i], want[i])
		}
	}

	all, err := Flatten(writeTemp(t, DefaultTOML))
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 30 || all[0].Key != "camera.zoom_percent" || all[29].Key != "style.browser_crop_top" {
		t.Errorf("unexpected default flatten: %d entries, first %v", len(all), all[0])
	}
}

func TestSettingsCarryTheirComments(t *testing.T) {
	settings, err := Settings(writeTemp(t, DefaultTOML))
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string]Setting{}
	for _, s := range settings {
		byKey[s.Key] = s
	}
	if got := byKey["camera.zoom_percent"].Help; got != "How far to zoom in, as a percentage. 0 disables zooming, 45 means 1.45x." {
		t.Errorf("zoom_percent help = %q", got)
	}
	// A comment block is attached only to the key directly below it.
	if got := byKey["camera.hold_s"].Help; got != "" {
		t.Errorf("hold_s help = %q, want empty", got)
	}
	if got := byKey["webcam.position"].Value; got != `"bottom-left"` {
		t.Errorf("position value = %q", got)
	}
}

func TestBackgroundPath(t *testing.T) {
	cfg := Default()
	if p, err := cfg.BackgroundPath(); p != "" || err != nil {
		t.Errorf("empty background = %q, %v", p, err)
	}
	cfg.Style.BackgroundImage = filepath.Join(t.TempDir(), "missing.png")
	if _, err := cfg.BackgroundPath(); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("missing image err = %v", err)
	}
	existing := writeTemp(t, "")
	cfg.Style.BackgroundImage = "  " + existing + " "
	if p, err := cfg.BackgroundPath(); p != existing || err != nil {
		t.Errorf("existing image = %q, %v", p, err)
	}
}

func TestResolveChrome(t *testing.T) {
	tests := []struct {
		chrome, bundle string
		want           Chrome
		crop           float32
	}{
		{"auto", "com.google.Chrome", ChromeBrowser, 88},
		{"auto", "com.google.chrome.canary", ChromeBrowser, 88},
		{"auto", "com.apple.Terminal", ChromeNone, 0},
		{"auto", "", ChromeNone, 0},
		{"none", "com.apple.Safari", ChromeNone, 0},
		{"WINDOW", "com.apple.Safari", ChromeWindow, 0},
		{"browser", "", ChromeBrowser, 88},
		{"nonsense", "com.apple.Safari", ChromeBrowser, 88},
	}
	for _, tt := range tests {
		cfg := Default()
		cfg.Style.Chrome = tt.chrome
		got, crop := cfg.ResolveChrome(tt.bundle)
		if got != tt.want || crop != tt.crop {
			t.Errorf("ResolveChrome(%q, %q) = %v, %v; want %v, %v", tt.chrome, tt.bundle, got, crop, tt.want, tt.crop)
		}
	}
	if IsBrowser("com.google.chromecast") {
		t.Error("a bundle id that merely starts with a browser's is not that browser")
	}
}

func TestClamping(t *testing.T) {
	cfg := Default()
	cfg.Camera.ZoomPercent = -10
	cfg.Camera.FollowDeadzonePercent = 80
	cfg.Camera.ZoomInS = math.NaN()
	cam := cfg.CameraConfig()
	if cam.MaxZoom != 1 || cam.FollowDeadzonePercent != 50 || cam.ZoomInS != 0.01 {
		t.Errorf("camera not clamped: %+v", cam)
	}
	cfg.Webcam.SizePercent = 0
	cfg.Webcam.PreviewS = 60
	w := cfg.ClampedWebcam()
	if w.SizePercent != 1 || w.PreviewS != 10 {
		t.Errorf("webcam not clamped: %+v", w)
	}
	cfg.Style.ShadowAlpha = 2
	if s := cfg.RenderStyle(""); s.ShadowAlpha != 1 || s.BgTop[3] != 1 {
		t.Errorf("style not clamped: %+v", s)
	}
}
