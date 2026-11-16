package m0

import (
	"bytes"
	"sort"
	"strconv"
	"unicode/utf8"
)

// maxSafeInteger is the CCJ safe integer bound (2^53-1).
const maxSafeInteger = 9007199254740991

type kind uint8

const (
	kindNull kind = iota
	kindBool
	kindInteger
	kindString
	kindArray
	kindObject
)

type member struct {
	key string
	val *value
}

// value is one parsed JSON node. Object members are sorted by the raw UTF-8
// bytes of their decoded names, which is Unicode scalar order.
type value struct {
	kind    kind
	boolean bool
	integer int64
	str     string
	items   []*value
	members []member
}

// limits bound one canonicalization. maxBytes applies to the input before any
// allocation; maxDepth counts nested containers with the outermost as 1;
// maxTokens counts every lexical token including punctuation and member names.
type limits struct {
	maxBytes  int
	maxDepth  int
	maxTokens int
}

var (
	requestLimits = limits{maxBytes: MaxRequestWire, maxDepth: 16, maxTokens: 1024}
	policyLimits  = limits{maxBytes: 16384, maxDepth: 16, maxTokens: 2048}
	entryLimits   = limits{maxBytes: 49152, maxDepth: 8, maxTokens: 512}
	configLimits  = limits{maxBytes: 4096, maxDepth: 3, maxTokens: 256}
)

func invalid(detail string) *Refusal { return refuse(CodeRequestInvalid, detail) }

// canonicalize strictly parses one JSON document within lim and returns its
// tree and CCJ-1 encoding: names sorted by Unicode scalar, array order kept,
// shortest integer spelling, no whitespace, only the prescribed string escapes.
// It refuses duplicate names at every depth (including escaped aliases), a byte
// order mark, invalid UTF-8, lone surrogates, non-integer numeric tokens,
// negative zero, integers outside the safe range, excess depth, excess tokens
// and trailing bytes. Nothing is allocated from an input longer than
// lim.maxBytes.
func canonicalize(b []byte, lim limits) (*value, []byte, error) {
	if len(b) > lim.maxBytes {
		return nil, nil, refuse(CodeRequestTooLarge, "document exceeds byte bound")
	}
	if len(b) == 0 {
		return nil, nil, invalid("empty document")
	}
	if bytes.HasPrefix(b, []byte{0xEF, 0xBB, 0xBF}) {
		return nil, nil, invalid("byte order mark")
	}
	p := &parser{b: b, lim: lim}
	p.space()
	v, err := p.value(0)
	if err != nil {
		return nil, nil, err
	}
	p.space()
	if p.i != len(p.b) {
		return nil, nil, invalid("trailing bytes")
	}
	return v, v.encode(nil), nil
}

type parser struct {
	b      []byte
	i      int
	lim    limits
	tokens int
}

func (p *parser) space() {
	for p.i < len(p.b) {
		switch p.b[p.i] {
		case ' ', '\t', '\n', '\r':
			p.i++
		default:
			return
		}
	}
}

func (p *parser) token() error {
	p.tokens++
	if p.tokens > p.lim.maxTokens {
		return invalid("token bound")
	}
	return nil
}

func (p *parser) value(depth int) (*value, error) {
	if p.i >= len(p.b) {
		return nil, invalid("unexpected end")
	}
	c := p.b[p.i]
	switch {
	case c == '{':
		return p.object(depth + 1)
	case c == '[':
		return p.array(depth + 1)
	case c == '"':
		if err := p.token(); err != nil {
			return nil, err
		}
		s, err := p.string()
		if err != nil {
			return nil, err
		}
		return &value{kind: kindString, str: s}, nil
	case c == '-' || (c >= '0' && c <= '9'):
		if err := p.token(); err != nil {
			return nil, err
		}
		return p.number()
	case c == 't' || c == 'f' || c == 'n':
		if err := p.token(); err != nil {
			return nil, err
		}
		return p.literal()
	}
	return nil, invalid("unexpected byte")
}

func (p *parser) object(depth int) (*value, error) {
	if depth > p.lim.maxDepth {
		return nil, invalid("depth bound")
	}
	if err := p.token(); err != nil {
		return nil, err
	}
	p.i++
	v := &value{kind: kindObject}
	p.space()
	if p.i < len(p.b) && p.b[p.i] == '}' {
		if err := p.token(); err != nil {
			return nil, err
		}
		p.i++
		return v, nil
	}
	seen := map[string]struct{}{}
	for {
		p.space()
		if p.i >= len(p.b) || p.b[p.i] != '"' {
			return nil, invalid("member name expected")
		}
		if err := p.token(); err != nil {
			return nil, err
		}
		key, err := p.string()
		if err != nil {
			return nil, err
		}
		if _, dup := seen[key]; dup {
			return nil, invalid("duplicate member name")
		}
		seen[key] = struct{}{}
		p.space()
		if p.i >= len(p.b) || p.b[p.i] != ':' {
			return nil, invalid("colon expected")
		}
		if err := p.token(); err != nil {
			return nil, err
		}
		p.i++
		p.space()
		child, err := p.value(depth)
		if err != nil {
			return nil, err
		}
		v.members = append(v.members, member{key: key, val: child})
		p.space()
		if p.i >= len(p.b) {
			return nil, invalid("unterminated object")
		}
		if err := p.token(); err != nil {
			return nil, err
		}
		switch p.b[p.i] {
		case ',':
			p.i++
		case '}':
			p.i++
			v.sortMembers()
			return v, nil
		default:
			return nil, invalid("comma or brace expected")
		}
	}
}

func (p *parser) array(depth int) (*value, error) {
	if depth > p.lim.maxDepth {
		return nil, invalid("depth bound")
	}
	if err := p.token(); err != nil {
		return nil, err
	}
	p.i++
	v := &value{kind: kindArray}
	p.space()
	if p.i < len(p.b) && p.b[p.i] == ']' {
		if err := p.token(); err != nil {
			return nil, err
		}
		p.i++
		return v, nil
	}
	for {
		p.space()
		child, err := p.value(depth)
		if err != nil {
			return nil, err
		}
		v.items = append(v.items, child)
		p.space()
		if p.i >= len(p.b) {
			return nil, invalid("unterminated array")
		}
		if err := p.token(); err != nil {
			return nil, err
		}
		switch p.b[p.i] {
		case ',':
			p.i++
		case ']':
			p.i++
			return v, nil
		default:
			return nil, invalid("comma or bracket expected")
		}
	}
}

// string decodes one JSON string starting at the opening quote. Raw bytes must
// be valid UTF-8 without control bytes; escapes are decoded, surrogate pairs
// combined and lone surrogates refused.
func (p *parser) string() (string, error) {
	p.i++
	var out []byte
	start := p.i
	for p.i < len(p.b) {
		c := p.b[p.i]
		switch {
		case c == '"':
			out = append(out, p.b[start:p.i]...)
			p.i++
			if !utf8.Valid(out) {
				return "", invalid("invalid UTF-8 in string")
			}
			return string(out), nil
		case c == '\\':
			out = append(out, p.b[start:p.i]...)
			p.i++
			r, err := p.escape()
			if err != nil {
				return "", err
			}
			out = utf8.AppendRune(out, r)
			start = p.i
		case c < 0x20:
			return "", invalid("control byte in string")
		default:
			p.i++
		}
	}
	return "", invalid("unterminated string")
}

func (p *parser) escape() (rune, error) {
	if p.i >= len(p.b) {
		return 0, invalid("unterminated escape")
	}
	c := p.b[p.i]
	p.i++
	switch c {
	case '"':
		return '"', nil
	case '\\':
		return '\\', nil
	case '/':
		return '/', nil
	case 'b':
		return '\b', nil
	case 'f':
		return '\f', nil
	case 'n':
		return '\n', nil
	case 'r':
		return '\r', nil
	case 't':
		return '\t', nil
	case 'u':
		hi, err := p.hex4()
		if err != nil {
			return 0, err
		}
		if hi >= 0xDC00 && hi <= 0xDFFF {
			return 0, invalid("lone low surrogate")
		}
		if hi >= 0xD800 && hi <= 0xDBFF {
			if p.i+1 >= len(p.b) || p.b[p.i] != '\\' || p.b[p.i+1] != 'u' {
				return 0, invalid("lone high surrogate")
			}
			p.i += 2
			lo, err := p.hex4()
			if err != nil {
				return 0, err
			}
			if lo < 0xDC00 || lo > 0xDFFF {
				return 0, invalid("lone high surrogate")
			}
			return rune(0x10000 + ((hi - 0xD800) << 10) + (lo - 0xDC00)), nil
		}
		return rune(hi), nil
	}
	return 0, invalid("unknown escape")
}

func (p *parser) hex4() (int, error) {
	if p.i+4 > len(p.b) {
		return 0, invalid("short unicode escape")
	}
	n := 0
	for k := 0; k < 4; k++ {
		c := p.b[p.i+k]
		var d int
		switch {
		case c >= '0' && c <= '9':
			d = int(c - '0')
		case c >= 'a' && c <= 'f':
			d = int(c-'a') + 10
		case c >= 'A' && c <= 'F':
			d = int(c-'A') + 10
		default:
			return 0, invalid("bad hex digit in escape")
		}
		n = n<<4 | d
	}
	p.i += 4
	return n, nil
}

func (p *parser) number() (*value, error) {
	start := p.i
	if p.b[p.i] == '-' {
		p.i++
	}
	if p.i >= len(p.b) {
		return nil, invalid("digit expected")
	}
	switch {
	case p.b[p.i] == '0':
		p.i++
	case p.b[p.i] >= '1' && p.b[p.i] <= '9':
		for p.i < len(p.b) && p.b[p.i] >= '0' && p.b[p.i] <= '9' {
			p.i++
		}
	default:
		return nil, invalid("digit expected")
	}
	if p.i < len(p.b) {
		switch p.b[p.i] {
		case '.', 'e', 'E':
			return nil, invalid("non-integer numeric token")
		}
	}
	text := string(p.b[start:p.i])
	if text == "-0" {
		return nil, invalid("negative zero")
	}
	if len(text) > 17 {
		return nil, invalid("integer outside safe range")
	}
	n, err := strconv.ParseInt(text, 10, 64)
	if err != nil || n > maxSafeInteger || n < -maxSafeInteger {
		return nil, invalid("integer outside safe range")
	}
	return &value{kind: kindInteger, integer: n}, nil
}

func (p *parser) literal() (*value, error) {
	for _, l := range []struct {
		text string
		v    value
	}{
		{"true", value{kind: kindBool, boolean: true}},
		{"false", value{kind: kindBool}},
		{"null", value{kind: kindNull}},
	} {
		if bytes.HasPrefix(p.b[p.i:], []byte(l.text)) {
			p.i += len(l.text)
			v := l.v
			return &v, nil
		}
	}
	return nil, invalid("unknown literal")
}

func (v *value) sortMembers() {
	sort.Slice(v.members, func(i, j int) bool { return v.members[i].key < v.members[j].key })
}

// encode appends the CCJ-1 bytes of v.
func (v *value) encode(dst []byte) []byte {
	switch v.kind {
	case kindNull:
		return append(dst, "null"...)
	case kindBool:
		if v.boolean {
			return append(dst, "true"...)
		}
		return append(dst, "false"...)
	case kindInteger:
		return strconv.AppendInt(dst, v.integer, 10)
	case kindString:
		return appendString(dst, v.str)
	case kindArray:
		dst = append(dst, '[')
		for i, item := range v.items {
			if i > 0 {
				dst = append(dst, ',')
			}
			dst = item.encode(dst)
		}
		return append(dst, ']')
	case kindObject:
		dst = append(dst, '{')
		for i, m := range v.members {
			if i > 0 {
				dst = append(dst, ',')
			}
			dst = appendString(dst, m.key)
			dst = append(dst, ':')
			dst = m.val.encode(dst)
		}
		return append(dst, '}')
	}
	return dst
}

const hexDigits = "0123456789abcdef"

// appendString emits the prescribed CCJ-1 escapes: quote, backslash, the five
// short control escapes, and \u00xx for the remaining control characters.
// Every other byte, including '/' and non-ASCII UTF-8, is emitted literally.
func appendString(dst []byte, s string) []byte {
	dst = append(dst, '"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '"':
			dst = append(dst, '\\', '"')
		case '\\':
			dst = append(dst, '\\', '\\')
		case '\b':
			dst = append(dst, '\\', 'b')
		case '\f':
			dst = append(dst, '\\', 'f')
		case '\n':
			dst = append(dst, '\\', 'n')
		case '\r':
			dst = append(dst, '\\', 'r')
		case '\t':
			dst = append(dst, '\\', 't')
		default:
			if c < 0x20 {
				dst = append(dst, '\\', 'u', '0', '0', hexDigits[c>>4], hexDigits[c&0xF])
			} else {
				dst = append(dst, c)
			}
		}
	}
	return append(dst, '"')
}

// member returns the named member of an object, or nil.
func (v *value) member(key string) *value {
	if v == nil || v.kind != kindObject {
		return nil
	}
	for _, m := range v.members {
		if m.key == key {
			return m.val
		}
	}
	return nil
}

// exactly refuses unless v is an object whose member names are exactly keys.
func (v *value) exactly(keys ...string) error {
	if v == nil || v.kind != kindObject {
		return invalid("object expected")
	}
	want := append([]string(nil), keys...)
	sort.Strings(want)
	if len(want) != len(v.members) {
		return invalid("member set differs from the closed form")
	}
	for i, k := range want {
		if v.members[i].key != k {
			return invalid("unknown or missing member: " + v.members[i].key)
		}
	}
	return nil
}

// without returns a shallow copy of an object without one member.
func (v *value) without(key string) *value {
	out := &value{kind: kindObject}
	for _, m := range v.members {
		if m.key != key {
			out.members = append(out.members, m)
		}
	}
	return out
}

func (v *value) text() (string, bool) {
	if v == nil || v.kind != kindString {
		return "", false
	}
	return v.str, true
}

func (v *value) number() (int64, bool) {
	if v == nil || v.kind != kindInteger {
		return 0, false
	}
	return v.integer, true
}

func (v *value) flag() (bool, bool) {
	if v == nil || v.kind != kindBool {
		return false, false
	}
	return v.boolean, true
}

// Builders for documents the dispatcher composes itself.
func object(ms ...member) *value {
	v := &value{kind: kindObject, members: append([]member(nil), ms...)}
	v.sortMembers()
	return v
}
func text(s string) *value   { return &value{kind: kindString, str: s} }
func integer(n int64) *value { return &value{kind: kindInteger, integer: n} }
func boolean(b bool) *value  { return &value{kind: kindBool, boolean: b} }
func array(items ...*value) *value {
	return &value{kind: kindArray, items: append([]*value(nil), items...)}
}

// stats counts objects, members and decoded string-value bytes (member names
// excluded) for the executor-entry work ceilings.
func (v *value) stats() (objects, members, stringBytes int) {
	switch v.kind {
	case kindString:
		return 0, 0, len(v.str)
	case kindArray:
		for _, item := range v.items {
			o, m, s := item.stats()
			objects += o
			members += m
			stringBytes += s
		}
	case kindObject:
		objects = 1
		members = len(v.members)
		for _, m := range v.members {
			o, mm, s := m.val.stats()
			objects += o
			members += mm
			stringBytes += s
		}
	}
	return objects, members, stringBytes
}
