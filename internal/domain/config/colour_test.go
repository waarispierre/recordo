package config

import "testing"

func TestHexRoundTripsThroughFloats(t *testing.T) {
	tests := []struct{ in, want string }{
		{"#FF0000", "[1.000, 0.000, 0.000]"},
		{"00ff00", "[0.000, 1.000, 0.000]"},
		{"#5C66C7", "[0.361, 0.400, 0.780]"},
	}
	for _, tt := range tests {
		if got, ok := RGBToTOML(tt.in); !ok || got != tt.want {
			t.Errorf("RGBToTOML(%q) = %q, %v; want %q", tt.in, got, ok, tt.want)
		}
	}
	if r, g, b, ok := ParseRGB("[1.0, 0.0, 0.0]"); !ok || r != 255 || g != 0 || b != 0 {
		t.Errorf("ParseRGB = %d %d %d %v", r, g, b, ok)
	}
}

func TestAcceptsFloatTriplesAndClamps(t *testing.T) {
	tests := []struct{ in, want string }{
		{"0.5, 0.5, 0.5", "[0.500, 0.500, 0.500]"},
		{"[2.0, -1.0, 0.5]", "[1.000, 0.000, 0.500]"},
	}
	for _, tt := range tests {
		if got, ok := RGBToTOML(tt.in); !ok || got != tt.want {
			t.Errorf("RGBToTOML(%q) = %q, %v; want %q", tt.in, got, ok, tt.want)
		}
	}
}

func TestRejectsNonsense(t *testing.T) {
	for _, in := range []string{"purple", "#12345", "#12345G", "+12345"} {
		if got, ok := RGBToTOML(in); ok {
			t.Errorf("RGBToTOML(%q) = %q, want rejection", in, got)
		}
	}
	for _, in := range []string{"[1.0, 0.0]", "1.0, 0.0, 0.0", "[a, b, c]"} {
		if _, _, _, ok := ParseRGB(in); ok {
			t.Errorf("ParseRGB(%q) accepted", in)
		}
	}
}

func TestIsColourKey(t *testing.T) {
	if !IsColourKey("style.pill_color") || IsColourKey("style.padding") {
		t.Error("IsColourKey misclassifies")
	}
}
