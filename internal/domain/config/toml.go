package config

// A small TOML reader that remembers where everything is in the source text.
//
// Why not a library: the Rust version uses toml_edit so that `config set` and the
// migration of new settings leave the user's comments and layout untouched. Go has no
// maintained format-preserving TOML editor; go-toml/v2's `unstable` parser exposes byte
// ranges but its API is explicitly unstable, and BurntSushi/toml keeps no positions at
// all. Every edit this package makes is a splice — replace one value's bytes, or insert
// whole lines copied from DefaultTOML — so all that is needed is the byte range of each
// value and statement, which this reader records. Text outside those ranges is never
// re-serialised, so comments, blank lines, key order and number formatting survive
// exactly as the user wrote them.
//
// It reads TOML 1.0 for everything this schema can hold: bare and quoted keys, all four
// string forms, integers (decimal, hex, octal, binary, underscores), floats (including
// inf/nan), booleans and multi-line arrays with comments. Datetimes, inline tables and
// arrays of tables are rejected with a clear error: none of them can appear in a valid
// recordo config.

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

type valueKind int

const (
	kindString valueKind = iota
	kindInteger
	kindFloat
	kindBool
	kindArray
)

func (k valueKind) String() string {
	switch k {
	case kindString:
		return "a string"
	case kindInteger:
		return "an integer"
	case kindFloat:
		return "a float"
	case kindBool:
		return "a boolean"
	case kindArray:
		return "an array"
	}
	return "an unknown value"
}

type tomlValue struct {
	kind    valueKind
	str     string
	integer int64
	float   float64
	boolean bool
	items   []tomlValue
}

// keyValue is one `key = value` statement.
type keyValue struct {
	key    string // dotted keys are joined with "."
	dotted bool
	value  tomlValue
	line   int
	// prefixStart is where the blank lines and comments leading up to this statement
	// begin — toml_edit's "decor prefix". Migration copies from here so a key arrives
	// with its explanation.
	prefixStart int
	valueStart  int
	valueEnd    int
	// lineEnd is just past the statement's newline, or len(text) at EOF without one.
	lineEnd int
}

// table is a `[header]` and the statements under it. The root table has name "".
type table struct {
	name        string
	prefixStart int
	lineEnd     int
	entries     []keyValue
}

func (t *table) get(key string) *keyValue {
	for i := range t.entries {
		if !t.entries[i].dotted && t.entries[i].key == key {
			return &t.entries[i]
		}
	}
	return nil
}

// lastStatementEnd is where a new statement appended to this table belongs.
func (t *table) lastStatementEnd() int {
	if n := len(t.entries); n > 0 {
		return t.entries[n-1].lineEnd
	}
	return t.lineEnd
}

type document struct {
	text string
	// tables in file order; tables[0] is always the root.
	tables []*table
	// end is just past the last statement in the file. Anything after it is trailing
	// comments and whitespace.
	end int
}

func (d *document) table(name string) *table {
	for _, t := range d.tables {
		if t.name == name {
			return t
		}
	}
	return nil
}

type parser struct {
	s    string
	pos  int
	line int
}

func (p *parser) errorf(format string, args ...any) error {
	return fmt.Errorf("line %d: %s", p.line, fmt.Sprintf(format, args...))
}

func (p *parser) eof() bool { return p.pos >= len(p.s) }

func (p *parser) peek() byte {
	if p.eof() {
		return 0
	}
	return p.s[p.pos]
}

func (p *parser) rest() string { return p.s[p.pos:] }

func (p *parser) skipSpace() {
	for !p.eof() && (p.s[p.pos] == ' ' || p.s[p.pos] == '\t') {
		p.pos++
	}
}

// newline consumes "\n" or "\r\n" if present.
func (p *parser) newline() bool {
	switch {
	case strings.HasPrefix(p.rest(), "\n"):
		p.pos++
	case strings.HasPrefix(p.rest(), "\r\n"):
		p.pos += 2
	default:
		return false
	}
	p.line++
	return true
}

func (p *parser) skipComment() {
	if p.peek() != '#' {
		return
	}
	for !p.eof() && p.s[p.pos] != '\n' && !strings.HasPrefix(p.rest(), "\r\n") {
		p.pos++
	}
}

// endOfLine consumes trailing whitespace, an optional comment and the newline, returning
// the offset just past it.
func (p *parser) endOfLine() (int, error) {
	p.skipSpace()
	p.skipComment()
	if p.eof() {
		return p.pos, nil
	}
	if !p.newline() {
		return 0, p.errorf("expected end of line, found %q", p.peek())
	}
	return p.pos, nil
}

// skipBlank skips whitespace, newlines and comments, as allowed inside arrays.
func (p *parser) skipBlank() {
	for {
		p.skipSpace()
		p.skipComment()
		if !p.newline() {
			return
		}
	}
}

func parseDocument(text string) (*document, error) {
	p := &parser{s: text, line: 1}
	root := &table{}
	doc := &document{text: text, tables: []*table{root}}
	cur := root
	prefixStart := 0

	for {
		p.skipSpace()
		if p.eof() {
			break
		}
		switch c := p.peek(); {
		case c == '#':
			p.skipComment()
			if !p.eof() && !p.newline() {
				return nil, p.errorf("unexpected %q", p.peek())
			}
		case c == '\n' || c == '\r':
			if !p.newline() {
				return nil, p.errorf("stray carriage return")
			}
		case c == '[':
			if strings.HasPrefix(p.rest(), "[[") {
				return nil, p.errorf("arrays of tables are not supported")
			}
			p.pos++
			p.skipSpace()
			parts, err := p.parseKey()
			if err != nil {
				return nil, err
			}
			p.skipSpace()
			if p.peek() != ']' {
				return nil, p.errorf("expected ] to close the table header")
			}
			p.pos++
			lineEnd, err := p.endOfLine()
			if err != nil {
				return nil, err
			}
			name := strings.Join(parts, ".")
			if doc.table(name) != nil || root.get(name) != nil {
				return nil, p.errorf("duplicate table [%s]", name)
			}
			cur = &table{name: name, prefixStart: prefixStart, lineEnd: lineEnd}
			doc.tables = append(doc.tables, cur)
			prefixStart, doc.end = lineEnd, lineEnd
		default:
			line := p.line
			parts, err := p.parseKey()
			if err != nil {
				return nil, err
			}
			p.skipSpace()
			if p.peek() != '=' {
				return nil, p.errorf("expected = after key %q", strings.Join(parts, "."))
			}
			p.pos++
			p.skipSpace()
			valueStart := p.pos
			v, err := p.parseValue()
			if err != nil {
				return nil, err
			}
			valueEnd := p.pos
			lineEnd, err := p.endOfLine()
			if err != nil {
				return nil, err
			}
			key := strings.Join(parts, ".")
			if cur.get(key) != nil {
				return nil, fmt.Errorf("line %d: duplicate key %q", line, key)
			}
			cur.entries = append(cur.entries, keyValue{
				key:         key,
				dotted:      len(parts) > 1,
				value:       v,
				line:        line,
				prefixStart: prefixStart,
				valueStart:  valueStart,
				valueEnd:    valueEnd,
				lineEnd:     lineEnd,
			})
			prefixStart, doc.end = lineEnd, lineEnd
		}
	}
	return doc, nil
}

func isBareKeyChar(c byte) bool {
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-'
}

// parseKey reads a possibly dotted key.
func (p *parser) parseKey() ([]string, error) {
	var parts []string
	for {
		p.skipSpace()
		var part string
		var err error
		switch p.peek() {
		case '"':
			part, err = p.basicString()
		case '\'':
			part, err = p.literalString()
		default:
			start := p.pos
			for !p.eof() && isBareKeyChar(p.s[p.pos]) {
				p.pos++
			}
			if start == p.pos {
				return nil, p.errorf("expected a key, found %q", p.peek())
			}
			part = p.s[start:p.pos]
		}
		if err != nil {
			return nil, err
		}
		parts = append(parts, part)
		p.skipSpace()
		if p.peek() != '.' {
			return parts, nil
		}
		p.pos++
	}
}

func (p *parser) parseValue() (tomlValue, error) {
	switch c := p.peek(); c {
	case '"':
		var s string
		var err error
		if strings.HasPrefix(p.rest(), `"""`) {
			s, err = p.multilineBasicString()
		} else {
			s, err = p.basicString()
		}
		return tomlValue{kind: kindString, str: s}, err
	case '\'':
		var s string
		var err error
		if strings.HasPrefix(p.rest(), "'''") {
			s, err = p.multilineLiteralString()
		} else {
			s, err = p.literalString()
		}
		return tomlValue{kind: kindString, str: s}, err
	case '[':
		return p.array()
	case '{':
		return tomlValue{}, p.errorf("inline tables are not supported")
	case 0:
		return tomlValue{}, p.errorf("expected a value")
	}

	start := p.pos
	for !p.eof() && !strings.ContainsRune(" \t\r\n,]}#", rune(p.s[p.pos])) {
		p.pos++
	}
	token := p.s[start:p.pos]
	if token == "" {
		return tomlValue{}, p.errorf("expected a value, found %q", p.peek())
	}
	v, err := scalar(token)
	if err != nil {
		return tomlValue{}, p.errorf("%v", err)
	}
	return v, nil
}

var (
	decimalInt = regexp.MustCompile(`^[+-]?(0|[1-9](_?[0-9])*)$`)
	prefixInt  = regexp.MustCompile(`^(0x[0-9A-Fa-f](_?[0-9A-Fa-f])*|0o[0-7](_?[0-7])*|0b[01](_?[01])*)$`)
	float      = regexp.MustCompile(`^[+-]?(0|[1-9](_?[0-9])*)(\.[0-9](_?[0-9])*)?([eE][+-]?[0-9](_?[0-9])*)?$`)
	special    = regexp.MustCompile(`^[+-]?(inf|nan)$`)
	date       = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}|^\d{2}:\d{2}`)
)

func scalar(token string) (tomlValue, error) {
	switch {
	case token == "true":
		return tomlValue{kind: kindBool, boolean: true}, nil
	case token == "false":
		return tomlValue{kind: kindBool}, nil
	case date.MatchString(token):
		return tomlValue{}, fmt.Errorf("dates and times are not supported")
	case decimalInt.MatchString(token):
		i, err := strconv.ParseInt(strings.ReplaceAll(token, "_", ""), 10, 64)
		if err != nil {
			return tomlValue{}, fmt.Errorf("integer %s out of range", token)
		}
		return tomlValue{kind: kindInteger, integer: i}, nil
	case prefixInt.MatchString(token):
		// Base 0 understands the 0x/0o/0b prefixes and the underscores between digits.
		i, err := strconv.ParseInt(token, 0, 64)
		if err != nil {
			return tomlValue{}, fmt.Errorf("integer %s out of range", token)
		}
		return tomlValue{kind: kindInteger, integer: i}, nil
	case special.MatchString(token):
		f := math.Inf(1)
		if strings.HasSuffix(token, "nan") {
			f = math.NaN()
		}
		if token[0] == '-' {
			f = -f
		}
		return tomlValue{kind: kindFloat, float: f}, nil
	case float.MatchString(token):
		f, err := strconv.ParseFloat(strings.ReplaceAll(token, "_", ""), 64)
		if err != nil && !math.IsInf(f, 0) {
			return tomlValue{}, fmt.Errorf("invalid float %s", token)
		}
		return tomlValue{kind: kindFloat, float: f}, nil
	}
	return tomlValue{}, fmt.Errorf("invalid value %q", token)
}

func (p *parser) array() (tomlValue, error) {
	p.pos++ // [
	v := tomlValue{kind: kindArray}
	for {
		p.skipBlank()
		if p.peek() == ']' {
			p.pos++
			return v, nil
		}
		item, err := p.parseValue()
		if err != nil {
			return tomlValue{}, err
		}
		v.items = append(v.items, item)
		p.skipBlank()
		switch p.peek() {
		case ',':
			p.pos++
		case ']':
			p.pos++
			return v, nil
		default:
			return tomlValue{}, p.errorf("expected , or ] in array, found %q", p.peek())
		}
	}
}

func isControl(r rune) bool {
	return r < 0x20 && r != '\t' || r == 0x7f
}

func (p *parser) basicString() (string, error) {
	p.pos++ // "
	var b strings.Builder
	for {
		if p.eof() {
			return "", p.errorf("unterminated string")
		}
		r, size := utf8.DecodeRuneInString(p.rest())
		switch {
		case r == '"':
			p.pos++
			return b.String(), nil
		case r == '\\':
			if err := p.escape(&b); err != nil {
				return "", err
			}
			continue
		case r == '\n' || r == '\r':
			return "", p.errorf("unterminated string")
		case isControl(r):
			return "", p.errorf("control character in string")
		}
		b.WriteRune(r)
		p.pos += size
	}
}

func (p *parser) escape(b *strings.Builder) error {
	p.pos++ // backslash
	if p.eof() {
		return p.errorf("unterminated escape")
	}
	c := p.s[p.pos]
	p.pos++
	switch c {
	case 'b':
		b.WriteByte('\b')
	case 't':
		b.WriteByte('\t')
	case 'n':
		b.WriteByte('\n')
	case 'f':
		b.WriteByte('\f')
	case 'r':
		b.WriteByte('\r')
	case '"':
		b.WriteByte('"')
	case '\\':
		b.WriteByte('\\')
	case 'u', 'U':
		n := 4
		if c == 'U' {
			n = 8
		}
		if p.pos+n > len(p.s) {
			return p.errorf("short unicode escape")
		}
		code, err := strconv.ParseUint(p.s[p.pos:p.pos+n], 16, 32)
		if err != nil || !utf8.ValidRune(rune(code)) {
			return p.errorf("invalid unicode escape \\%c%s", c, p.s[p.pos:p.pos+n])
		}
		b.WriteRune(rune(code))
		p.pos += n
	default:
		return p.errorf("invalid escape \\%c", c)
	}
	return nil
}

func (p *parser) literalString() (string, error) {
	p.pos++ // '
	start := p.pos
	for {
		if p.eof() {
			return "", p.errorf("unterminated string")
		}
		r, size := utf8.DecodeRuneInString(p.rest())
		switch {
		case r == '\'':
			s := p.s[start:p.pos]
			p.pos++
			return s, nil
		case r == '\n' || r == '\r':
			return "", p.errorf("unterminated string")
		case isControl(r):
			return "", p.errorf("control character in string")
		}
		p.pos += size
	}
}

// closing reports whether a multi-line string closes here, consuming the delimiter and
// returning up to two quote characters that belong to the content (`""""` is legal).
func (p *parser) closing(quote string) (extra string, closed bool, err error) {
	if !strings.HasPrefix(p.rest(), quote) {
		return "", false, nil
	}
	n := 0
	for p.pos+n < len(p.s) && p.s[p.pos+n] == quote[0] {
		n++
	}
	if n > 5 {
		return "", false, p.errorf("too many quotes closing a string")
	}
	p.pos += n
	return strings.Repeat(quote[:1], n-3), true, nil
}

func (p *parser) multilineBasicString() (string, error) {
	p.pos += 3
	p.newline() // a newline straight after the opening delimiter is trimmed
	var b strings.Builder
	for {
		if p.eof() {
			return "", p.errorf("unterminated string")
		}
		extra, closed, err := p.closing(`"""`)
		if err != nil {
			return "", err
		}
		if closed {
			b.WriteString(extra)
			return b.String(), nil
		}
		if p.peek() == '\\' {
			// A line-ending backslash swallows the newline and the whitespace after it.
			save := p.pos
			p.pos++
			p.skipSpace()
			if p.newline() {
				for {
					p.skipSpace()
					if !p.newline() {
						break
					}
				}
				continue
			}
			p.pos = save
			if err := p.escape(&b); err != nil {
				return "", err
			}
			continue
		}
		if p.newline() {
			b.WriteByte('\n')
			continue
		}
		r, size := utf8.DecodeRuneInString(p.rest())
		if isControl(r) {
			return "", p.errorf("control character in string")
		}
		b.WriteRune(r)
		p.pos += size
	}
}

func (p *parser) multilineLiteralString() (string, error) {
	p.pos += 3
	p.newline()
	var b strings.Builder
	for {
		if p.eof() {
			return "", p.errorf("unterminated string")
		}
		extra, closed, err := p.closing("'''")
		if err != nil {
			return "", err
		}
		if closed {
			b.WriteString(extra)
			return b.String(), nil
		}
		if p.newline() {
			b.WriteByte('\n')
			continue
		}
		r, size := utf8.DecodeRuneInString(p.rest())
		if isControl(r) {
			return "", p.errorf("control character in string")
		}
		b.WriteRune(r)
		p.pos += size
	}
}
