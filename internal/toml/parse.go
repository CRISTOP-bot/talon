// Package toml implements the subset of TOML v1.0 that Talon configuration
// files use: comments, bare/quoted/dotted keys, tables, arrays of tables,
// inline tables, strings, integers, floats and booleans.
//
// Keeping this in-tree avoids a third-party dependency and guarantees the CLI
// builds offline on every platform.
package toml

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Kind enumerates the value types the parser can produce.
type Kind int

const (
	KindInvalid Kind = iota
	KindString
	KindInt
	KindFloat
	KindBool
	KindArray
	KindTable
)

// Value is a parsed TOML value. Tables are maps of pointers so that nested
// documents can be mutated in place (config writes, CLI setters).
type Value struct {
	Kind  Kind
	Str   string
	Int   int64
	Float float64
	Bool  bool
	Array []*Value
	Table map[string]*Value

	// line is the 1-based source line, used for diagnostics.
	line int
	// defined marks tables created by an explicit [header], so declaring the
	// same header twice is an error while implicit parents are fine.
	defined bool
}

// Error is a parse error carrying the offending line number.
type Error struct {
	Line int
	Msg  string
}

func (e *Error) Error() string {
	if e.Line > 0 {
		return fmt.Sprintf("line %d: %s", e.Line, e.Msg)
	}
	return e.Msg
}

func errAt(line int, format string, args ...any) *Error {
	return &Error{Line: line, Msg: fmt.Sprintf(format, args...)}
}

// Constructors used when building configuration documents.

func String(s string) *Value { return &Value{Kind: KindString, Str: s} }
func Int(i int64) *Value     { return &Value{Kind: KindInt, Int: i} }
func Float(f float64) *Value { return &Value{Kind: KindFloat, Float: f} }
func Bool(b bool) *Value     { return &Value{Kind: KindBool, Bool: b} }

// Strings builds an array value from a slice of strings.
func Strings(items []string) *Value {
	arr := &Value{Kind: KindArray}
	for _, s := range items {
		arr.Array = append(arr.Array, String(s))
	}
	return arr
}

// NewTable builds an empty table value.
func NewTable() *Value { return &Value{Kind: KindTable, Table: map[string]*Value{}} }

// Get returns the value at a dotted path and whether it existed.
func (v *Value) Get(path string) (*Value, bool) {
	cur := v
	if cur == nil {
		return nil, false
	}
	if path == "" {
		return cur, true
	}
	for _, part := range strings.Split(path, ".") {
		if cur == nil || cur.Kind != KindTable {
			return nil, false
		}
		next, ok := cur.Table[part]
		if !ok {
			return nil, false
		}
		cur = next
	}
	return cur, true
}

// Has reports whether a dotted path exists.
func (v *Value) Has(path string) bool { _, ok := v.Get(path); return ok }

// Set stores val at a dotted path, creating intermediate tables.
func (v *Value) Set(path string, val *Value) {
	parts := strings.Split(path, ".")
	cur := v
	for i, p := range parts {
		if cur.Kind != KindTable || cur.Table == nil {
			cur.Kind = KindTable
			cur.Table = map[string]*Value{}
		}
		if i == len(parts)-1 {
			cur.Table[p] = val
			return
		}
		next, ok := cur.Table[p]
		if !ok || next.Kind != KindTable {
			next = NewTable()
			next.defined = true
			cur.Table[p] = next
		}
		cur = next
	}
}

// Delete removes a dotted path if it is present.
func (v *Value) Delete(path string) {
	parts := strings.Split(path, ".")
	cur := v
	for i, p := range parts {
		if cur == nil || cur.Kind != KindTable {
			return
		}
		if i == len(parts)-1 {
			delete(cur.Table, p)
			return
		}
		next, ok := cur.Table[p]
		if !ok {
			return
		}
		cur = next
	}
}

// Keys returns the sorted key names of a table.
func (v *Value) Keys() []string {
	if v == nil || v.Kind != KindTable {
		return nil
	}
	out := make([]string, 0, len(v.Table))
	for k := range v.Table {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Clone returns a deep copy of the value.
func (v *Value) Clone() *Value {
	if v == nil {
		return nil
	}
	cp := *v
	if v.Array != nil {
		cp.Array = make([]*Value, len(v.Array))
		for i, it := range v.Array {
			cp.Array[i] = it.Clone()
		}
	}
	if v.Table != nil {
		cp.Table = make(map[string]*Value, len(v.Table))
		for k, val := range v.Table {
			cp.Table[k] = val.Clone()
		}
	}
	return &cp
}

// AsString returns the string content.
func (v *Value) AsString() (string, bool) {
	if v != nil && v.Kind == KindString {
		return v.Str, true
	}
	return "", false
}

// AsInt returns the integer content (floats are truncated).
func (v *Value) AsInt() (int64, bool) {
	if v == nil {
		return 0, false
	}
	switch v.Kind {
	case KindInt:
		return v.Int, true
	case KindFloat:
		return int64(v.Float), true
	}
	return 0, false
}

// AsBool returns the boolean content.
func (v *Value) AsBool() (bool, bool) {
	if v != nil && v.Kind == KindBool {
		return v.Bool, true
	}
	return false, false
}

// AsFloat returns the float content.
func (v *Value) AsFloat() (float64, bool) {
	if v == nil {
		return 0, false
	}
	switch v.Kind {
	case KindFloat:
		return v.Float, true
	case KindInt:
		return float64(v.Int), true
	}
	return 0, false
}

// AsStrings returns an array of strings. A bare string is promoted to a
// single-element slice so `key = "a"` and `key = ["a"]` behave alike.
func (v *Value) AsStrings() ([]string, bool) {
	if v == nil {
		return nil, false
	}
	switch v.Kind {
	case KindString:
		return []string{v.Str}, true
	case KindArray:
		out := make([]string, 0, len(v.Array))
		for _, it := range v.Array {
			if it == nil || it.Kind != KindString {
				return nil, false
			}
			out = append(out, it.Str)
		}
		return out, true
	}
	return nil, false
}

// String renders the value in TOML syntax (used by the encoder and /config).
func (v *Value) String() string {
	if v == nil {
		return ""
	}
	switch v.Kind {
	case KindString:
		return quote(v.Str)
	case KindInt:
		return strconv.FormatInt(v.Int, 10)
	case KindFloat:
		return strconv.FormatFloat(v.Float, 'f', -1, 64)
	case KindBool:
		if v.Bool {
			return "true"
		}
		return "false"
	case KindArray:
		parts := make([]string, 0, len(v.Array))
		for _, it := range v.Array {
			parts = append(parts, it.String())
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case KindTable:
		return "{ " + strings.Join(v.inlinePairs(), ", ") + " }"
	default:
		return ""
	}
}

func (v *Value) inlinePairs() []string {
	var out []string
	for _, k := range v.Keys() {
		out = append(out, quote(k)+" = "+v.Table[k].String())
	}
	return out
}

func quote(s string) string {
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
			if r < 0x20 {
				b.WriteString(fmt.Sprintf(`\u%04X`, r))
				continue
			}
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// Parse reads a TOML document into a root table value.
func Parse(input string) (*Value, error) {
	p := &parser{src: input, line: 1, root: NewTable(), curTable: nil}
	p.root.defined = true
	p.curTable = p.root
	if err := p.run(); err != nil {
		return nil, err
	}
	return p.root, nil
}

type parser struct {
	src      string
	pos      int
	line     int
	root     *Value
	curTable *Value
}

func (p *parser) run() error {
	for {
		p.skipIgnorable()
		if p.eof() {
			return nil
		}
		switch p.src[p.pos] {
		case '[':
			if err := p.parseHeader(); err != nil {
				return err
			}
		default:
			if err := p.parseKeyValue(); err != nil {
				return err
			}
		}
	}
}

func (p *parser) eof() bool { return p.pos >= len(p.src) }

func (p *parser) skipComment() {
	for !p.eof() && p.src[p.pos] != '\n' {
		p.pos++
	}
}

// skipIgnorable advances over whitespace, newlines (counted for diagnostics)
// and comments that sit between statements.
func (p *parser) skipIgnorable() {
	for !p.eof() {
		switch c := p.src[p.pos]; {
		case c == '\n':
			p.line++
			p.pos++
		case c == ' ' || c == '\t' || c == '\r':
			p.pos++
		case c == '#':
			p.skipComment()
		default:
			return
		}
	}
}

func (p *parser) skipSpaces() {
	for !p.eof() && (p.src[p.pos] == ' ' || p.src[p.pos] == '\t') {
		p.pos++
	}
}

func (p *parser) parseHeader() error {
	line := p.line
	p.pos++ // '['
	arrayTable := false
	if !p.eof() && p.src[p.pos] == '[' {
		arrayTable = true
		p.pos++
	}
	p.skipSpaces()
	path, err := p.parseKeyPath()
	if err != nil {
		return err
	}
	p.skipSpaces()
	if p.eof() || p.src[p.pos] != ']' {
		return errAt(line, "expected ']' to close table header")
	}
	p.pos++
	if arrayTable {
		if p.eof() || p.src[p.pos] != ']' {
			return errAt(line, "expected ']]' to close array-of-tables header")
		}
		p.pos++
	}
	p.skipSpaces()
	if !p.eof() && p.src[p.pos] == '#' {
		p.skipComment()
	}
	return p.resolveHeader(path, arrayTable, line)
}

// resolveHeader creates or re-opens the table addressed by the header and
// stores it as the destination for subsequent key/value pairs.
func (p *parser) resolveHeader(path []string, arrayTable bool, line int) error {
	cur := p.root
	for i, seg := range path {
		last := i == len(path)-1
		existing, ok := cur.Table[seg]
		if !ok {
			next := NewTable()
			next.defined = true
			cur.Table[seg] = next
			if last && arrayTable {
				elem := NewTable()
				elem.defined = true
				next.Kind = KindArray
				next.Array = []*Value{elem}
				cur = elem
			} else {
				cur = next
			}
			continue
		}
		switch {
		case last && arrayTable:
			if existing.Kind != KindArray {
				return errAt(line, "%q is not an array of tables", strings.Join(path, "."))
			}
			elem := NewTable()
			elem.defined = true
			existing.Array = append(existing.Array, elem)
			cur = elem
		case existing.Kind == KindArray && len(existing.Array) > 0:
			cur = existing.Array[len(existing.Array)-1]
		case existing.Kind == KindTable:
			if last && existing.defined {
				return errAt(line, "table %q declared twice", strings.Join(path, "."))
			}
			cur = existing
		default:
			return errAt(line, "key %q is not a table", seg)
		}
	}
	p.curTable = cur
	return nil
}

func (p *parser) parseKeyValue() error {
	line := p.line
	path, err := p.parseKeyPath()
	if err != nil {
		return err
	}
	p.skipSpaces()
	if p.eof() || p.src[p.pos] != '=' {
		return errAt(line, "expected '=' after key %q", strings.Join(path, "."))
	}
	p.pos++
	p.skipSpaces()
	val, err := p.parseValue()
	if err != nil {
		return err
	}
	p.skipSpaces()
	if !p.eof() && p.src[p.pos] == '#' {
		p.skipComment()
	}
	if !p.eof() && p.src[p.pos] != '\n' {
		return errAt(p.line, "unexpected text after value")
	}
	tbl := p.curTable
	if tbl == nil {
		return errAt(line, "value outside of any table")
	}
	cur := tbl
	for i, seg := range path {
		if i == len(path)-1 {
			if _, dup := cur.Table[seg]; dup {
				return errAt(line, "duplicate key %q", strings.Join(path, "."))
			}
			val.line = line
			cur.Table[seg] = val
			return nil
		}
		next, ok := cur.Table[seg]
		if !ok {
			next = NewTable()
			cur.Table[seg] = next
		}
		if next.Kind != KindTable {
			return errAt(line, "key %q is not a table", seg)
		}
		cur = next
	}
	return nil
}

func (p *parser) parseKeyPath() ([]string, error) {
	var out []string
	for {
		p.skipSpaces()
		key, err := p.parseKey()
		if err != nil {
			return nil, err
		}
		out = append(out, key)
		p.skipSpaces()
		if !p.eof() && p.src[p.pos] == '.' {
			p.pos++
			continue
		}
		return out, nil
	}
}

func (p *parser) parseKey() (string, error) {
	if p.eof() {
		return "", errAt(p.line, "unexpected end of input while reading a key")
	}
	switch p.src[p.pos] {
	case '"':
		return p.parseBasicString()
	case '\'':
		return p.parseLiteralString()
	}
	start := p.pos
	for !p.eof() && isBareKeyChar(p.src[p.pos]) {
		p.pos++
	}
	if start == p.pos {
		return "", errAt(p.line, "invalid character %q in key", string(p.src[p.pos]))
	}
	return p.src[start:p.pos], nil
}

func isBareKeyChar(c byte) bool {
	return c == '_' || c == '-' || (c >= '0' && c <= '9') ||
		(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func (p *parser) parseValue() (*Value, error) {
	if p.eof() {
		return nil, errAt(p.line, "expected a value")
	}
	switch c := p.src[p.pos]; {
	case c == '"' || c == '\'':
		s, err := p.parseString()
		if err != nil {
			return nil, err
		}
		return String(s), nil
	case c == '[':
		return p.parseArray()
	case c == '{':
		return p.parseInlineTable()
	case c == 't' || c == 'f':
		return p.parseBool()
	default:
		return p.parseNumber()
	}
}

func (p *parser) parseString() (string, error) {
	if p.src[p.pos] == '"' {
		if p.hasPrefix(`"""`) {
			return p.parseMultilineBasic()
		}
		return p.parseBasicString()
	}
	if p.hasPrefix("'''") {
		return p.parseMultilineLiteral()
	}
	return p.parseLiteralString()
}

func (p *parser) hasPrefix(s string) bool {
	return strings.HasPrefix(p.src[p.pos:], s)
}

func (p *parser) parseBasicString() (string, error) {
	line := p.line
	p.pos++ // opening quote
	var b strings.Builder
	for {
		if p.eof() {
			return "", errAt(line, "unterminated string")
		}
		c := p.src[p.pos]
		switch c {
		case '"':
			p.pos++
			return b.String(), nil
		case '\n':
			return "", errAt(line, "newline inside a single-line string")
		case '\\':
			p.pos++
			r, err := p.readEscape()
			if err != nil {
				return "", err
			}
			b.WriteString(r)
		default:
			b.WriteByte(c)
			p.pos++
		}
	}
}

func (p *parser) parseMultilineBasic() (string, error) {
	line := p.line
	p.pos += 3
	if !p.eof() && p.src[p.pos] == '\r' {
		p.pos++
	}
	if !p.eof() && p.src[p.pos] == '\n' {
		p.pos++
		p.line++
	}
	var b strings.Builder
	for {
		if p.eof() {
			return "", errAt(line, "unterminated multi-line string")
		}
		if p.hasPrefix(`"""`) {
			p.pos += 3
			return b.String(), nil
		}
		c := p.src[p.pos]
		if c == '\n' {
			p.line++
			b.WriteByte(c)
			p.pos++
			continue
		}
		if c == '\\' {
			p.pos++
			j := p.pos
			for j < len(p.src) && (p.src[j] == ' ' || p.src[j] == '\t' || p.src[j] == '\r') {
				j++
			}
			if j < len(p.src) && p.src[j] == '\n' {
				// Line-ending backslash: trim the newline and indentation.
				p.pos = j
				continue
			}
			r, err := p.readEscape()
			if err != nil {
				return "", err
			}
			b.WriteString(r)
			continue
		}
		b.WriteByte(c)
		p.pos++
	}
}

func (p *parser) parseLiteralString() (string, error) {
	line := p.line
	p.pos++
	start := p.pos
	for !p.eof() {
		c := p.src[p.pos]
		if c == '\'' {
			s := p.src[start:p.pos]
			p.pos++
			return s, nil
		}
		if c == '\n' {
			return "", errAt(line, "newline inside a single-line string")
		}
		p.pos++
	}
	return "", errAt(line, "unterminated literal string")
}

func (p *parser) parseMultilineLiteral() (string, error) {
	line := p.line
	p.pos += 3
	if !p.eof() && p.src[p.pos] == '\r' {
		p.pos++
	}
	if !p.eof() && p.src[p.pos] == '\n' {
		p.pos++
		p.line++
	}
	start := p.pos
	for {
		if p.eof() {
			return "", errAt(line, "unterminated multi-line literal string")
		}
		if p.hasPrefix("'''") {
			s := p.src[start:p.pos]
			p.pos += 3
			return s, nil
		}
		if p.src[p.pos] == '\n' {
			p.line++
		}
		p.pos++
	}
}

func (p *parser) readEscape() (string, error) {
	if p.eof() {
		return "", errAt(p.line, "unterminated escape sequence")
	}
	c := p.src[p.pos]
	p.pos++
	switch c {
	case 'b':
		return "\b", nil
	case 't':
		return "\t", nil
	case 'n':
		return "\n", nil
	case 'f':
		return "\f", nil
	case 'r':
		return "\r", nil
	case '"':
		return `"`, nil
	case '\\':
		return `\`, nil
	case 'u', 'U':
		width := 4
		if c == 'U' {
			width = 8
		}
		if p.pos+width > len(p.src) {
			return "", errAt(p.line, "truncated unicode escape")
		}
		hex := p.src[p.pos : p.pos+width]
		n, err := strconv.ParseUint(hex, 16, 32)
		if err != nil {
			return "", errAt(p.line, "invalid unicode escape %q", hex)
		}
		if !utf8.ValidRune(rune(n)) {
			return "", errAt(p.line, "unicode escape %q is not a valid rune", hex)
		}
		p.pos += width
		return string(rune(n)), nil
	default:
		return "", errAt(p.line, `unknown escape sequence \%c`, c)
	}
}

func (p *parser) parseArray() (*Value, error) {
	line := p.line
	p.pos++ // '['
	out := &Value{Kind: KindArray, Array: []*Value{}}
	for {
		p.skipIgnorable()
		if p.eof() {
			return nil, errAt(line, "unterminated array")
		}
		if p.src[p.pos] == ']' {
			p.pos++
			return out, nil
		}
		v, err := p.parseValue()
		if err != nil {
			return nil, err
		}
		out.Array = append(out.Array, v)
		p.skipIgnorable()
		if p.eof() {
			return nil, errAt(line, "unterminated array")
		}
		switch p.src[p.pos] {
		case ',':
			p.pos++
		case ']':
			p.pos++
			return out, nil
		default:
			return nil, errAt(p.line, "expected ',' or ']' in array")
		}
	}
}

func (p *parser) parseInlineTable() (*Value, error) {
	line := p.line
	p.pos++ // '{'
	out := NewTable()
	p.skipSpaces()
	if !p.eof() && p.src[p.pos] == '}' {
		p.pos++
		return out, nil
	}
	for {
		p.skipSpaces()
		path, err := p.parseKeyPath()
		if err != nil {
			return nil, err
		}
		p.skipSpaces()
		if p.eof() || p.src[p.pos] != '=' {
			return nil, errAt(line, "expected '=' in inline table")
		}
		p.pos++
		p.skipSpaces()
		val, err := p.parseValue()
		if err != nil {
			return nil, err
		}
		cur := out
		for i, seg := range path {
			if i == len(path)-1 {
				cur.Table[seg] = val
				break
			}
			next, ok := cur.Table[seg]
			if !ok || next.Kind != KindTable {
				next = NewTable()
				cur.Table[seg] = next
			}
			cur = next
		}
		p.skipSpaces()
		if p.eof() {
			return nil, errAt(line, "unterminated inline table")
		}
		switch p.src[p.pos] {
		case ',':
			p.pos++
		case '}':
			p.pos++
			return out, nil
		default:
			return nil, errAt(p.line, "expected ',' or '}' in inline table")
		}
	}
}

func (p *parser) parseBool() (*Value, error) {
	switch {
	case p.hasPrefix("true"):
		p.pos += 4
		return Bool(true), nil
	case p.hasPrefix("false"):
		p.pos += 5
		return Bool(false), nil
	}
	return nil, errAt(p.line, "invalid boolean literal")
}

func (p *parser) parseNumber() (*Value, error) {
	line := p.line
	start := p.pos
	for !p.eof() {
		c := p.src[p.pos]
		if c == '\n' || c == '#' || c == ',' || c == ']' || c == '}' || c == ' ' || c == '\t' || c == '\r' {
			break
		}
		p.pos++
	}
	raw := p.src[start:p.pos]
	if raw == "" {
		return nil, errAt(line, "expected a value")
	}
	cleaned := strings.ReplaceAll(raw, "_", "")
	if i, err := strconv.ParseInt(cleaned, 0, 64); err == nil {
		return Int(i), nil
	}
	if f, err := strconv.ParseFloat(cleaned, 64); err == nil {
		return Float(f), nil
	}
	// Dates and times are kept as strings: Talon does not use them, but
	// accepting them keeps hand-written files parseable.
	if looksLikeDate(raw) {
		return String(raw), nil
	}
	return nil, errAt(line, "invalid value %q", raw)
}

func looksLikeDate(s string) bool {
	if len(s) < 8 {
		return false
	}
	digits := 0
	for _, r := range s {
		if unicode.IsDigit(r) {
			digits++
			continue
		}
		if !strings.ContainsRune("-:.TZ+ ", r) {
			return false
		}
	}
	return digits >= 6
}
