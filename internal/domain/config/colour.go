package config

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// ParseRGB parses a `[r, g, b]` setting back into 0-255 components, for showing a swatch.
func ParseRGB(value string) (r, g, b uint8, ok bool) {
	inner, found := strings.CutPrefix(strings.TrimSpace(value), "[")
	if !found {
		return 0, 0, 0, false
	}
	inner, found = strings.CutSuffix(inner, "]")
	if !found {
		return 0, 0, 0, false
	}
	parts, ok := parseTriple(inner)
	if !ok {
		return 0, 0, 0, false
	}
	return toByte(parts[0]), toByte(parts[1]), toByte(parts[2]), true
}

func toByte(v float32) uint8 {
	c := clamp(v, 0, 1)
	if c != c {
		return 0 // Rust's saturating `as u8` turns NaN into 0
	}
	return uint8(math.Round(float64(c * 255)))
}

// parseTriple parses exactly three comma-separated f32 values.
func parseTriple(s string) ([3]float32, bool) {
	fields := strings.Split(s, ",")
	if len(fields) != 3 {
		return [3]float32{}, false
	}
	var out [3]float32
	for i, f := range fields {
		v, ok := parseRustFloat(strings.TrimSpace(f), 32)
		if !ok {
			return [3]float32{}, false
		}
		out[i] = float32(v)
	}
	return out, true
}

// RGBToTOML accepts `#RRGGBB`, `RRGGBB` or `r,g,b` (0-1 floats) and returns TOML array
// text.
//
// Hex is what people actually have to hand — design tools, CSS, brand guidelines all
// speak it, whereas three normalised floats have to be worked out by hand.
func RGBToTOML(input string) (string, bool) {
	text := strings.TrimSpace(input)
	hex := strings.TrimLeft(text, "#")
	if len(hex) == 6 {
		if v, err := strconv.ParseUint(hex, 16, 32); err == nil {
			r, g, b := float32(v>>16&0xff), float32(v>>8&0xff), float32(v&0xff)
			return formatTriple(r/255, g/255, b/255), true
		}
	}
	// Already an array, or bare floats.
	inner := strings.TrimRight(strings.TrimLeft(text, "["), "]")
	parts, ok := parseTriple(inner)
	if !ok {
		return "", false
	}
	return formatTriple(clamp(parts[0], 0, 1), clamp(parts[1], 0, 1), clamp(parts[2], 0, 1)), true
}

func formatTriple(r, g, b float32) string {
	return fmt.Sprintf("[%.3f, %.3f, %.3f]", r, g, b)
}

// IsColourKey reports whether key holds an RGB colour, so callers can accept hex for it.
func IsColourKey(key string) bool {
	switch key {
	case "style.bg_top", "style.bg_bottom", "style.chrome_bg", "style.pill_color":
		return true
	}
	return false
}
