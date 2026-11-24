package config

import "fmt"

// setter stores one decoded value into its field.
type setter func(*Config, tomlValue) error

// schema lists every setting the file may contain. Anything else is an error, matching
// serde's deny_unknown_fields in the Rust version: a misspelt key should be reported, not
// silently ignored.
var schema = map[string]map[string]setter{
	"camera": {
		"zoom_percent":            f64(func(c *Config) *float64 { return &c.Camera.ZoomPercent }),
		"zoom_in_s":               f64(func(c *Config) *float64 { return &c.Camera.ZoomInS }),
		"hold_s":                  f64(func(c *Config) *float64 { return &c.Camera.HoldS }),
		"zoom_out_s":              f64(func(c *Config) *float64 { return &c.Camera.ZoomOutS }),
		"follow_hz":               f64(func(c *Config) *float64 { return &c.Camera.FollowHz }),
		"follow_deadzone_percent": f64(func(c *Config) *float64 { return &c.Camera.FollowDeadzonePercent }),
	},
	"audio": {
		"microphone": boolean(func(c *Config) *bool { return &c.Audio.Microphone }),
		"device":     str(func(c *Config) *string { return &c.Audio.Device }),
	},
	"webcam": {
		"enabled":       boolean(func(c *Config) *bool { return &c.Webcam.Enabled }),
		"device":        str(func(c *Config) *string { return &c.Webcam.Device }),
		"position":      str(func(c *Config) *string { return &c.Webcam.Position }),
		"inset":         f32(func(c *Config) *float32 { return &c.Webcam.Inset }),
		"offset":        f32s(func(c *Config) []float32 { return c.Webcam.Offset[:] }),
		"size_percent":  f32(func(c *Config) *float32 { return &c.Webcam.SizePercent }),
		"shape":         str(func(c *Config) *string { return &c.Webcam.Shape }),
		"corner_radius": f32(func(c *Config) *float32 { return &c.Webcam.CornerRadius }),
		"mirror":        boolean(func(c *Config) *bool { return &c.Webcam.Mirror }),
		"preview_s":     f32(func(c *Config) *float32 { return &c.Webcam.PreviewS }),
	},
	"style": {
		"padding":          f32(func(c *Config) *float32 { return &c.Style.Padding }),
		"corner_radius":    f32(func(c *Config) *float32 { return &c.Style.CornerRadius }),
		"shadow_offset":    f32s(func(c *Config) []float32 { return c.Style.ShadowOffset[:] }),
		"shadow_blur":      f32(func(c *Config) *float32 { return &c.Style.ShadowBlur }),
		"shadow_alpha":     f32(func(c *Config) *float32 { return &c.Style.ShadowAlpha }),
		"bg_top":           f32s(func(c *Config) []float32 { return c.Style.BgTop[:] }),
		"bg_bottom":        f32s(func(c *Config) []float32 { return c.Style.BgBottom[:] }),
		"background_image": str(func(c *Config) *string { return &c.Style.BackgroundImage }),
		"chrome":           str(func(c *Config) *string { return &c.Style.Chrome }),
		"chrome_bg":        f32s(func(c *Config) []float32 { return c.Style.ChromeBg[:] }),
		"pill_color":       f32s(func(c *Config) []float32 { return c.Style.PillColor[:] }),
		"browser_crop_top": f32(func(c *Config) *float32 { return &c.Style.BrowserCropTop }),
	},
}

func decode(doc *document) (Config, error) {
	cfg := Default()
	for _, t := range doc.tables {
		if t.name == "" {
			if len(t.entries) > 0 {
				kv := t.entries[0]
				return Config{}, fmt.Errorf("line %d: unknown setting %q outside any section", kv.line, kv.key)
			}
			continue
		}
		section, ok := schema[t.name]
		if !ok {
			return Config{}, fmt.Errorf("unknown section [%s]", t.name)
		}
		for _, kv := range t.entries {
			set, ok := section[kv.key]
			if !ok {
				return Config{}, fmt.Errorf("line %d: unknown setting %s.%s", kv.line, t.name, kv.key)
			}
			if err := set(&cfg, kv.value); err != nil {
				return Config{}, fmt.Errorf("line %d: %s.%s: %w", kv.line, t.name, kv.key, err)
			}
		}
	}
	return cfg, nil
}

// number accepts integers as well as floats, as serde does for float fields: `zoom_percent
// = 30` is as valid as `30.0`.
func (v tomlValue) number() (float64, error) {
	switch v.kind {
	case kindFloat:
		return v.float, nil
	case kindInteger:
		return float64(v.integer), nil
	}
	return 0, fmt.Errorf("expected a number, found %s", v.kind)
}

func f64(field func(*Config) *float64) setter {
	return func(c *Config, v tomlValue) error {
		n, err := v.number()
		if err != nil {
			return err
		}
		*field(c) = n
		return nil
	}
}

func f32(field func(*Config) *float32) setter {
	return func(c *Config, v tomlValue) error {
		n, err := v.number()
		if err != nil {
			return err
		}
		*field(c) = float32(n)
		return nil
	}
}

// f32s fills a fixed-length array; the length must match exactly.
func f32s(field func(*Config) []float32) setter {
	return func(c *Config, v tomlValue) error {
		dst := field(c)
		if v.kind != kindArray {
			return fmt.Errorf("expected an array of %d numbers, found %s", len(dst), v.kind)
		}
		if len(v.items) != len(dst) {
			return fmt.Errorf("expected an array of %d numbers, found %d", len(dst), len(v.items))
		}
		for i, item := range v.items {
			n, err := item.number()
			if err != nil {
				return fmt.Errorf("element %d: %w", i, err)
			}
			dst[i] = float32(n)
		}
		return nil
	}
}

func boolean(field func(*Config) *bool) setter {
	return func(c *Config, v tomlValue) error {
		if v.kind != kindBool {
			return fmt.Errorf("expected true or false, found %s", v.kind)
		}
		*field(c) = v.boolean
		return nil
	}
}

func str(field func(*Config) *string) setter {
	return func(c *Config, v tomlValue) error {
		if v.kind != kindString {
			return fmt.Errorf("expected a string, found %s", v.kind)
		}
		*field(c) = v.str
		return nil
	}
}
