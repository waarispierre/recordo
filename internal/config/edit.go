package config

import (
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
)

// Entry is one setting as `section.key` with its value as written in the file.
type Entry struct {
	Key   string
	Value string
}

// Flatten lists every setting in file order, for `config show`/`get`.
func Flatten(path string) ([]Entry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	doc, err := parseDocument(string(data))
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	var out []Entry
	for _, t := range doc.tables {
		for _, kv := range t.entries {
			key := kv.key
			if t.name != "" {
				key = t.name + "." + kv.key
			}
			out = append(out, Entry{Key: key, Value: doc.text[kv.valueStart:kv.valueEnd]})
		}
	}
	return out, nil
}

// SetValue writes one `section.key`, preserving the file's comments and layout.
//
// Only the value's own bytes are replaced; the key, the spacing around `=` and any
// trailing comment on the line are left as they were.
func SetValue(path, key, value string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	text := string(data)
	doc, err := parseDocument(text)
	if err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}

	section, field, ok := strings.Cut(key, ".")
	if !ok {
		return fmt.Errorf("expected section.key, got %q", key)
	}
	t := doc.table(section)
	if section == "" || t == nil {
		return fmt.Errorf("no such section: %s", section)
	}
	kv := t.get(field)
	if kv == nil {
		return fmt.Errorf("no such setting: %s", key)
	}

	updated := text[:kv.valueStart] + inferValue(value) + text[kv.valueEnd:]
	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// inferValue turns typed text into TOML, inferring the type so `config set` needs no
// type annotations. The rules follow the Rust version: bool, then integer, then float,
// then a bracketed array, and anything else is a string.
func inferValue(raw string) string {
	text := strings.TrimSpace(raw)
	if text == "true" || text == "false" {
		return text
	}
	if i, err := strconv.ParseInt(text, 10, 64); err == nil {
		return strconv.FormatInt(i, 10)
	}
	if f, ok := parseRustFloat(text, 64); ok {
		return formatFloat(f)
	}
	if strings.HasPrefix(text, "[") {
		// Arrays (colours, shadow offsets) arrive as "[0.1, 0.2, 0.3]".
		inner := strings.TrimRight(strings.TrimLeft(text, "["), "]")
		var items []string
		for _, part := range strings.Split(inner, ",") {
			p := strings.TrimSpace(part)
			if p == "" {
				continue
			}
			if i, err := strconv.ParseInt(p, 10, 64); err == nil {
				items = append(items, strconv.FormatInt(i, 10))
			} else if f, ok := parseRustFloat(p, 64); ok {
				items = append(items, formatFloat(f))
			} else {
				items = append(items, formatString(strings.Trim(p, `"`)))
			}
		}
		return "[" + strings.Join(items, ", ") + "]"
	}
	return formatString(strings.Trim(text, `"`))
}

// parseRustFloat accepts what Rust's str::parse::<f64> accepts. Go's ParseFloat is more
// liberal — hex floats and underscores — and those must stay strings here, as they would
// in the Rust version.
func parseRustFloat(s string, bits int) (float64, bool) {
	if s == "" || strings.ContainsAny(s, "_xXpP") {
		return 0, false
	}
	f, err := strconv.ParseFloat(s, bits)
	if err != nil {
		// Out of range saturates to ±inf (or 0) in Rust rather than failing; Go returns
		// the same value but with ErrRange attached.
		if ne, ok := err.(*strconv.NumError); ok && ne.Err == strconv.ErrRange {
			return f, true
		}
		return 0, false
	}
	return f, true
}

// formatFloat matches toml_edit's float output: always a decimal point, never an
// exponent, and inf/nan spelt the TOML way.
func formatFloat(f float64) string {
	switch {
	case math.IsNaN(f):
		if math.Signbit(f) {
			return "-nan"
		}
		return "nan"
	case math.IsInf(f, 1):
		return "inf"
	case math.IsInf(f, -1):
		return "-inf"
	case f == 0:
		if math.Signbit(f) {
			return "-0.0"
		}
		return "0.0"
	}
	s := strconv.FormatFloat(f, 'f', -1, 64)
	if math.Mod(f, 1) == 0 {
		s += ".0"
	}
	return s
}

// formatString quotes s as a TOML string: basic when nothing needs escaping, literal when
// it holds quotes or backslashes but no apostrophes, escaped basic otherwise.
func formatString(s string) string {
	hasControl := strings.ContainsFunc(s, isControl)
	if !hasControl && !strings.ContainsAny(s, `"\`) {
		return `"` + s + `"`
	}
	if !hasControl && !strings.Contains(s, "'") {
		return "'" + s + "'"
	}
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\t':
			b.WriteString(`\t`)
		case '\r':
			b.WriteString(`\r`)
		default:
			if isControl(r) {
				fmt.Fprintf(&b, `\u%04X`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

// Setting is one setting as shown in the interactive menu.
type Setting struct {
	// Key is `section.key`.
	Key   string
	Value string
	// Help is the comment lines above it in the file, joined into one sentence.
	Help string
}

// Settings lists settings with the file's own comments attached as help text.
//
// The comments are read from the raw text rather than the parsed document: they are
// already written for a human, so there is no second copy to keep in sync.
func Settings(path string) ([]Setting, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	var out []Setting
	var section string
	var pending []string
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			pending = pending[:0]
			continue
		}
		if comment, ok := strings.CutPrefix(trimmed, "#"); ok {
			pending = append(pending, strings.TrimSpace(comment))
			continue
		}
		if strings.HasPrefix(trimmed, "[") {
			section = strings.Trim(trimmed, "[]")
			pending = pending[:0]
			continue
		}
		if key, value, ok := strings.Cut(trimmed, "="); ok {
			out = append(out, Setting{
				Key:   section + "." + strings.TrimSpace(key),
				Value: strings.TrimSpace(value),
				Help:  strings.Join(pending, " "),
			})
			pending = pending[:0]
		}
	}
	return out, nil
}

// addMissingSettings adds any setting present in the defaults but absent from text,
// returning the new contents and whether anything changed.
//
// Existing values and comments are left exactly as they are; only gaps are filled, and
// each added key brings its explanatory comment with it. Placement follows toml_edit, so
// the Rust and Go versions migrate a file identically: missing keys go after the last key
// of their section, missing sections go after the last statement in the file (before any
// trailing comments).
func addMissingSettings(text string) (string, bool, error) {
	doc, err := parseDocument(text)
	if err != nil {
		return "", false, err
	}
	defaults, err := parseDocument(DefaultTOML)
	if err != nil {
		return "", false, fmt.Errorf("parse built-in defaults: %w", err)
	}

	type insertion struct {
		at   int
		text string
	}
	var inserts []insertion
	var newSections strings.Builder

	for _, def := range defaults.tables {
		if def.name == "" {
			continue
		}
		target := doc.table(def.name)
		var block strings.Builder
		for _, kv := range def.entries {
			if target != nil && target.get(kv.key) != nil {
				continue
			}
			// Carry the comment across, so a migrated file reads like a fresh one.
			block.WriteString(withNewline(DefaultTOML[kv.prefixStart:kv.lineEnd]))
		}
		switch {
		case target == nil:
			// Copy the default section's header and the blank line before it too, so a
			// whole new section arrives with its spacing rather than jammed against the
			// previous one.
			newSections.WriteString(withNewline(DefaultTOML[def.prefixStart:def.lineEnd]))
			newSections.WriteString(block.String())
		case block.Len() > 0:
			inserts = append(inserts, insertion{at: target.lastStatementEnd(), text: block.String()})
		}
	}
	if newSections.Len() > 0 {
		inserts = append(inserts, insertion{at: doc.end, text: newSections.String()})
	}
	if len(inserts) == 0 {
		return text, false, nil
	}
	// Sections need not be in the default order in the user's file.
	sort.SliceStable(inserts, func(i, j int) bool { return inserts[i].at < inserts[j].at })

	var out strings.Builder
	last := 0
	for _, ins := range inserts {
		// A file whose last statement has no trailing newline needs one before anything
		// can follow it.
		if ins.at > 0 && text[ins.at-1] != '\n' {
			ins.text = "\n" + ins.text
		}
		out.WriteString(text[last:ins.at])
		out.WriteString(ins.text)
		last = ins.at
	}
	out.WriteString(text[last:])
	return out.String(), true, nil
}

func withNewline(s string) string {
	if strings.HasSuffix(s, "\n") {
		return s
	}
	return s + "\n"
}
