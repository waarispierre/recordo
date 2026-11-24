// Package config holds the user-editable settings, loaded from
// ~/Library/Application Support/recordo/config.toml (see session.ConfigPath).
//
// Everything has a default, so a missing or partial file is fine — the file only needs
// to name the values being overridden.
//
// The package is deliberately self-contained: it describes the camera model, render style
// and chrome mode with its own plain types rather than importing the packages that consume
// them, so those packages map from these values instead of config depending on them.
package config

import (
	"errors"
	"fmt"
	"github.com/waarispierre/recordo/internal/camera"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Config is the whole settings file.
type Config struct {
	Camera CameraSettings
	Audio  AudioSettings
	Webcam WebcamSettings
	Style  StyleSettings
}

// CameraSettings is the [camera] section.
type CameraSettings struct {
	// How far to zoom in, as a percentage. 0 disables zooming, 45 means 1.45x.
	ZoomPercent float64
	ZoomInS     float64
	HoldS       float64
	ZoomOutS    float64
	// Cursor-follow spring frequency in Hz; higher tracks more tightly.
	FollowHz float64
	// Radius of the deadzone around the camera centre, as a percentage of the visible
	// width. Cursor movement inside it is ignored. 0 follows continuously.
	FollowDeadzonePercent float64
}

// AudioSettings is the [audio] section: voice over. Off by default: a recording should
// never pick up the room by surprise.
type AudioSettings struct {
	// Record the microphone alongside the screen.
	Microphone bool
	// Which microphone, matched on name. Empty uses the system default.
	Device string
}

// WebcamSettings is the [webcam] section: picture-in-picture. Off by default, same
// reasoning as voice over: never inferred, only turned on deliberately.
type WebcamSettings struct {
	Enabled bool
	// Which camera, matched on name. Empty uses the system default.
	Device string
	// "bottom-left", "bottom-right", "top-left" or "top-right".
	Position string
	// Distance from that corner, in points.
	Inset float32
	// Nudge from the corner, in points, for when the inset alone is not enough.
	Offset [2]float32
	// Height as a percentage of the exported frame's height.
	SizePercent float32
	// "circle" or "rect".
	Shape string
	// Corner rounding in points. Only used when shape is "rect".
	CornerRadius float32
	// Mirror the image, so it reads the way a mirror does rather than how others see you.
	Mirror bool
	// Seconds to hold a live framing preview on screen before a recording starts. The
	// same preview stays in the corner while recording. 0 turns both off.
	PreviewS float32
}

// StyleSettings is the [style] section.
type StyleSettings struct {
	// Gap between the background edge and the window, in points.
	Padding      float32
	CornerRadius float32
	ShadowOffset [2]float32
	ShadowBlur   float32
	ShadowAlpha  float32
	// Gradient endpoints, RGB 0-1. Ignored when BackgroundImage is set.
	BgTop    [3]float32
	BgBottom [3]float32
	// Image behind the window, scaled to cover. Empty means use the gradient.
	BackgroundImage string
	// "auto", "none", "window", or "browser".
	//
	// "auto" decides per recorded app: browsers get the mock browser frame with their
	// own tab/address strip cropped away; native apps get none, since they already draw
	// a title bar and adding another yields a window inside a window.
	Chrome    string
	ChromeBg  [3]float32
	PillColor [3]float32
	// Height in points of real browser chrome (tabs + address bar) to crop off before
	// compositing. Varies by browser and whether a bookmarks bar is shown.
	BrowserCropTop float32
}

// CameraConfig is the camera model input. It is the camera package's own type, so the
// defaults live in one place.
type CameraConfig = camera.Config

// Chrome is the synthetic window frame drawn around the capture.
type Chrome int

const (
	// ChromeNone draws no title bar — the capture fills the whole rounded window.
	ChromeNone Chrome = iota
	// ChromeWindow draws a macOS title bar with traffic lights.
	ChromeWindow
	// ChromeBrowser draws a macOS title bar with traffic lights and a blank URL pill.
	ChromeBrowser
)

func (c Chrome) String() string {
	switch c {
	case ChromeNone:
		return "none"
	case ChromeWindow:
		return "window"
	case ChromeBrowser:
		return "browser"
	}
	return fmt.Sprintf("Chrome(%d)", int(c))
}

// Style is the resolved render style, colours as RGBA. It mirrors the renderer's style;
// that package maps from this.
type Style struct {
	Padding      float32
	CornerRadius float32
	ShadowOffset [2]float32
	ShadowBlur   float32
	ShadowAlpha  float32
	BgTop        [4]float32
	BgBottom     [4]float32
	Chrome       Chrome
	ChromeBg     [4]float32
	PillColor    [4]float32
}

// DefaultStyle is the renderer's default look.
func DefaultStyle() Style {
	return Style{
		Padding:      48.0,
		CornerRadius: 13.0,
		ShadowOffset: [2]float32{0.0, 9.0},
		ShadowBlur:   22.0,
		ShadowAlpha:  0.45,
		BgTop:        [4]float32{0.36, 0.40, 0.78, 1.0},
		BgBottom:     [4]float32{0.60, 0.36, 0.72, 1.0},
		Chrome:       ChromeBrowser,
		ChromeBg:     [4]float32{0.16, 0.16, 0.18, 1.0},
		PillColor:    [4]float32{0.24, 0.24, 0.27, 1.0},
	}
}

// Default is the configuration used for anything the file does not name.
func Default() Config {
	cam := camera.DefaultConfig()
	style := DefaultStyle()
	return Config{
		Camera: CameraSettings{
			ZoomPercent:           (cam.MaxZoom - 1.0) * 100.0,
			ZoomInS:               cam.ZoomInS,
			HoldS:                 cam.HoldS,
			ZoomOutS:              cam.ZoomOutS,
			FollowHz:              cam.FollowHz,
			FollowDeadzonePercent: cam.FollowDeadzonePercent,
		},
		Webcam: WebcamSettings{
			Position:     "bottom-left",
			Inset:        32.0,
			SizePercent:  18.0,
			Shape:        "circle",
			CornerRadius: 24.0,
			Mirror:       true,
			PreviewS:     3.0,
		},
		Style: StyleSettings{
			Padding:        style.Padding,
			CornerRadius:   style.CornerRadius,
			ShadowOffset:   style.ShadowOffset,
			ShadowBlur:     style.ShadowBlur,
			ShadowAlpha:    style.ShadowAlpha,
			BgTop:          rgb(style.BgTop),
			BgBottom:       rgb(style.BgBottom),
			Chrome:         "auto",
			ChromeBg:       rgb(style.ChromeBg),
			PillColor:      rgb(style.PillColor),
			BrowserCropTop: 88.0,
		},
	}
}

func rgb(c [4]float32) [3]float32  { return [3]float32{c[0], c[1], c[2]} }
func rgba(c [3]float32) [4]float32 { return [4]float32{c[0], c[1], c[2], 1.0} }

// LoadOrCreate loads the config, writing a commented default file if none exists yet.
func LoadOrCreate(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		if err := os.WriteFile(path, []byte(DefaultTOML), 0o644); err != nil {
			return Config{}, fmt.Errorf("write default config to %s: %w", path, err)
		}
		fmt.Printf("wrote default config to %s\n", path)
		return Default(), nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("read %s: %w", path, err)
	}
	text := string(data)

	// A config written by an earlier version is missing anything added since. Decoding
	// would silently default those fields, but `config show`, `config set` and the
	// settings pane all read the file itself — so a new option would be invisible and
	// unsettable until the file was deleted. Fill the gaps instead.
	migrated, changed, err := addMissingSettings(text)
	if err != nil {
		return Config{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if changed {
		if err := os.WriteFile(path, []byte(migrated), 0o644); err != nil {
			return Config{}, fmt.Errorf("update %s: %w", path, err)
		}
		text = migrated
	}

	cfg, err := Parse(text)
	if err != nil {
		return Config{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return cfg, nil
}

// Parse decodes config text. Unknown sections and keys are errors, so a typo is reported
// rather than silently ignored; anything absent keeps its default.
func Parse(text string) (Config, error) {
	doc, err := parseDocument(text)
	if err != nil {
		return Config{}, err
	}
	return decode(doc)
}

// BackgroundPath is the background image, or "" if none is set.
//
// Returns an error rather than silently falling back to the gradient: a typo in the path
// should be reported, not quietly ignored.
func (c Config) BackgroundPath() (string, error) {
	raw := strings.TrimSpace(c.Style.BackgroundImage)
	if raw == "" {
		return "", nil
	}
	expanded := raw
	if rest, ok := strings.CutPrefix(raw, "~/"); ok {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("could not locate your home directory: %w", err)
		}
		expanded = filepath.Join(home, rest)
	}
	if _, err := os.Stat(expanded); err != nil {
		return "", fmt.Errorf("background_image %s does not exist — set it to \"\" to use the gradient", expanded)
	}
	return expanded, nil
}

// CameraConfig is the camera model input, clamped to sane ranges.
func (c Config) CameraConfig() CameraConfig {
	s := c.Camera
	return CameraConfig{
		MaxZoom:  1.0 + fmax(s.ZoomPercent, 0)/100.0,
		ZoomInS:  fmax(s.ZoomInS, 0.01),
		HoldS:    fmax(s.HoldS, 0),
		ZoomOutS: fmax(s.ZoomOutS, 0.01),
		FollowHz: fmax(s.FollowHz, 0.05),
		// Above 50% the boundary would exceed the visible frame and the camera could
		// never move at all.
		FollowDeadzonePercent: clamp(s.FollowDeadzonePercent, 0, 50),
	}
}

// ClampedWebcam is the webcam settings, clamped to sane ranges.
func (c Config) ClampedWebcam() WebcamSettings {
	w := c.Webcam
	w.Inset = fmax(w.Inset, 0)
	w.SizePercent = clamp(w.SizePercent, 1, 100)
	w.CornerRadius = fmax(w.CornerRadius, 0)
	// A pre-roll is a pause before the thing you asked for; long enough to check framing
	// is the point, long enough to be annoying is not.
	w.PreviewS = clamp(w.PreviewS, 0, 10)
	return w
}

// RenderStyle is the style for a specific recorded app; the chrome may depend on which
// app it was. bundleID is "" when unknown.
func (c Config) RenderStyle(bundleID string) Style {
	s := c.Style
	chrome, _ := c.ResolveChrome(bundleID)
	return Style{
		Padding:      fmax(s.Padding, 0),
		CornerRadius: fmax(s.CornerRadius, 0),
		ShadowOffset: s.ShadowOffset,
		ShadowBlur:   fmax(s.ShadowBlur, 0.01),
		ShadowAlpha:  clamp(s.ShadowAlpha, 0, 1),
		BgTop:        rgba(s.BgTop),
		BgBottom:     rgba(s.BgBottom),
		Chrome:       chrome,
		ChromeBg:     rgba(s.ChromeBg),
		PillColor:    rgba(s.PillColor),
	}
}

// ResolveChrome is the chrome mode plus how many points to crop off the top of the
// capture. The crop is nonzero only for browsers, where removing the real tab strip and
// address bar is what lets the drawn frame stand in for them — and is what strips tabs,
// bookmarks, profile avatar and URL history from the recording.
func (c Config) ResolveChrome(bundleID string) (Chrome, float32) {
	crop := fmax(c.Style.BrowserCropTop, 0)
	switch asciiLower(c.Style.Chrome) {
	case "none":
		return ChromeNone, 0
	case "window":
		return ChromeWindow, 0
	case "browser":
		return ChromeBrowser, crop
	}
	if bundleID != "" && IsBrowser(bundleID) {
		return ChromeBrowser, crop
	}
	return ChromeNone, 0
}

// browsers are bundle identifiers of browsers whose own chrome should be replaced.
var browsers = []string{
	"com.apple.safari",
	"com.apple.safaritechnologypreview",
	"com.google.chrome",
	"com.google.chrome.canary",
	"com.microsoft.edgemac",
	"company.thebrowser.browser",
	"company.thebrowser.dia",
	"org.mozilla.firefox",
	"com.brave.browser",
	"com.vivaldi.vivaldi",
	"com.operasoftware.opera",
}

// IsBrowser reports whether bundleID is a browser whose own chrome should be replaced.
// Sub-identifiers (e.g. a helper's "com.google.chrome.foo") count too.
func IsBrowser(bundleID string) bool {
	id := asciiLower(bundleID)
	for _, b := range browsers {
		if id == b || strings.HasPrefix(id, b+".") {
			return true
		}
	}
	return false
}

// asciiLower lowercases ASCII letters only, matching Rust's to_ascii_lowercase; Unicode
// case folding could map non-ASCII input onto a keyword.
func asciiLower(s string) string {
	return strings.Map(func(r rune) rune {
		if 'A' <= r && r <= 'Z' {
			return r + ('a' - 'A')
		}
		return r
	}, s)
}

// fmax follows Rust's f64::max: a NaN operand is ignored rather than propagated, so a
// "nan" in the file falls back to the floor instead of poisoning the render.
func fmax[T float32 | float64](a, b T) T {
	if a != a {
		return b
	}
	if b != b {
		return a
	}
	if a > b {
		return a
	}
	return b
}

// clamp follows Rust's clamp: NaN passes through unchanged.
func clamp[T float32 | float64](v, lo, hi T) T {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
