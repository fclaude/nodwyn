package scheme

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ErrIncomplete reports balanced-but-unfinished input (an open list or
// string at EOF). The REPL uses it to keep reading.
var ErrIncomplete = errors.New("incomplete input")

// ParseError is a syntax error with a position.
type ParseError struct {
	Line, Col int
	Msg       string
}

func (e *ParseError) Error() string { return fmt.Sprintf("%d:%d: %s", e.Line, e.Col, e.Msg) }

// Read parses every top-level datum in src.
func Read(src string) ([]Value, error) {
	r := &reader{src: src, line: 1, col: 1}
	var out []Value
	for {
		r.skipSpace()
		if r.eof() {
			if r.unterminated {
				return out, ErrIncomplete
			}
			return out, nil
		}
		v, err := r.datum()
		if err != nil {
			return out, err
		}
		out = append(out, v)
	}
}

// ReadOne parses exactly one datum and rejects trailing input.
func ReadOne(src string) (Value, error) {
	vs, err := Read(src)
	if err != nil {
		return Value{}, err
	}
	if len(vs) != 1 {
		return Value{}, fmt.Errorf("expected one datum, got %d", len(vs))
	}
	return vs[0], nil
}

type reader struct {
	src          string
	pos          int
	line, col    int
	unterminated bool // an open #| comment ran to EOF
	depth        int  // lists, vectors and quotes open around the datum being read
}

func (r *reader) eof() bool { return r.pos >= len(r.src) }

func (r *reader) peek() rune {
	if r.eof() {
		return 0
	}
	c, _ := utf8.DecodeRuneInString(r.src[r.pos:])
	return c
}

func (r *reader) next() rune {
	c, n := utf8.DecodeRuneInString(r.src[r.pos:])
	r.pos += n
	if c == '\n' {
		r.line++
		r.col = 1
	} else {
		r.col++
	}
	return c
}

func (r *reader) errf(format string, args ...any) error {
	return &ParseError{Line: r.line, Col: r.col, Msg: fmt.Sprintf(format, args...)}
}

func (r *reader) skipSpace() {
	for !r.eof() {
		c := r.peek()
		switch {
		case c == '#' && r.startsWith("#|"):
			if !r.blockComment() {
				r.unterminated = true
				return
			}
		case c == ';':
			for !r.eof() && r.peek() != '\n' {
				r.next()
			}
		case unicode.IsSpace(c):
			r.next()
		default:
			return
		}
	}
}

// startsWith reports whether the input at the current position begins with s.
func (r *reader) startsWith(s string) bool {
	return len(r.src[r.pos:]) >= len(s) && r.src[r.pos:r.pos+len(s)] == s
}

// blockComment skips a "#| ... |#" block comment, updating the line and
// column. It returns false for an unterminated comment, which consumes the
// rest of the input so the caller sees EOF and reports incomplete input.
func (r *reader) blockComment() bool {
	rest := r.src[r.pos+2:]
	j := strings.Index(rest, "|#")
	consumed := 2 + j + 2
	if j < 0 {
		consumed = len(r.src) - r.pos
	}
	seg := r.src[r.pos : r.pos+consumed]
	for _, ch := range seg {
		if ch == '\n' {
			r.line++
			r.col = 1
		} else {
			r.col++
		}
	}
	r.pos += consumed
	return j >= 0
}

func isDelimiter(c rune) bool {
	return c == 0 || unicode.IsSpace(c) || c == '(' || c == ')' || c == '"' || c == ';' || c == '\''
}

func (r *reader) datum() (Value, error) {
	if r.depth >= MaxDepth {
		return Value{}, r.errf("nested more than %d deep", MaxDepth)
	}
	r.depth++
	defer func() { r.depth-- }()
	r.skipSpace()
	if r.eof() {
		return Value{}, ErrIncomplete
	}
	c := r.peek()
	switch c {
	case '(':
		r.next()
		return r.list()
	case ')':
		return Value{}, r.errf("unexpected )")
	case '[', ']', '{', '}':
		return Value{}, r.errf("%c is not valid syntax (only parentheses delimit lists)", c)
	case '\'':
		r.next()
		return r.abbrev("quote")
	case '`':
		r.next()
		return r.abbrev("quasiquote")
	case ',':
		r.next()
		if r.peek() == '@' {
			r.next()
			return r.abbrev("unquote-splicing")
		}
		return r.abbrev("unquote")
	case '"':
		if r.startsWith(`"""`) {
			return r.rawString()
		}
		r.next()
		return r.str()
	case '#':
		if r.startsWith("#(") {
			r.next() // '#'
			return r.vector()
		}
		return r.atom()
	}
	return r.atom()
}

// vector reads "#( e … )" into a vector value.
func (r *reader) vector() (Value, error) {
	r.next() // '('
	var items []Value
	for {
		r.skipSpace()
		if r.eof() {
			return Value{}, ErrIncomplete
		}
		c := r.peek()
		if c == ')' {
			r.next()
			break
		}
		v, err := r.datum()
		if err != nil {
			return Value{}, err
		}
		items = append(items, v)
	}
	return Vec(items...), nil
}

func (r *reader) abbrev(name string) (Value, error) {
	d, err := r.datum()
	if err != nil {
		return Value{}, err
	}
	return List(Sym(name), d), nil
}

func (r *reader) list() (Value, error) {
	var items []Value
	tail := Nil
	for {
		r.skipSpace()
		if r.eof() {
			return Value{}, ErrIncomplete
		}
		c := r.peek()
		if c == ')' {
			r.next()
			break
		}
		if c == '.' {
			// A lone "." is the dotted-pair marker; ".5" or "..." are atoms.
			save := *r
			r.next()
			if isDelimiter(r.peek()) {
				if len(items) == 0 {
					return Value{}, r.errf("dot without a preceding element")
				}
				t, err := r.datum()
				if err != nil {
					return Value{}, err
				}
				tail = t
				r.skipSpace()
				if r.eof() {
					return Value{}, ErrIncomplete
				}
				if r.peek() != ')' {
					return Value{}, r.errf("expected ) after dotted tail")
				}
				r.next()
				break
			}
			*r = save
		}
		v, err := r.datum()
		if err != nil {
			return Value{}, err
		}
		items = append(items, v)
	}
	out := tail
	for i := len(items) - 1; i >= 0; i-- {
		out = Cons(items[i], out)
	}
	return out, nil
}

// rawString reads a """ … """ block: no escapes, so the text between the
// delimiters is exactly what the file holds. It is for prose that belongs
// in a program — instructions, tool documentation, templates — where
// backslash escaping and one-line-per-string are the wrong tools.
//
// When the opening delimiter is followed by a newline the block is treated
// as an indented text block: that first newline goes, the indentation
// shared by every non-blank line and by the closing delimiter's line is
// removed, and the newline before the closing delimiter goes. So a block
// nested inside a define reads at the indentation it is written at and
// produces text with none. Anything else is verbatim.
func (r *reader) rawString() (Value, error) {
	for i := 0; i < 3; i++ {
		r.next()
	}
	start := r.pos
	for !r.startsWith(`"""`) {
		if r.eof() {
			return Value{}, ErrIncomplete
		}
		r.next()
	}
	body := r.src[start:r.pos]
	closing := r.col
	for i := 0; i < 3; i++ {
		r.next()
	}
	return Str(textBlock(body, closing)), nil
}

// textBlock applies the indented-block rule. closingCol is the 1-based
// column the closing delimiter starts at, which is the indentation the
// author chose for the block as a whole.
func textBlock(body string, closingCol int) string {
	after, isBlock := strings.CutPrefix(body, "\n")
	if !isBlock {
		return body
	}
	lines := strings.Split(after, "\n")
	// The closing delimiter sits on its own line when the body ends with a
	// newline; that trailing empty line is the delimiter's own indentation
	// and never part of the text.
	if len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	indent := closingCol - 1
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if n := len(line) - len(strings.TrimLeft(line, " \t")); n < indent {
			indent = n
		}
	}
	if indent < 0 {
		indent = 0
	}
	for i, line := range lines {
		if len(line) < indent {
			lines[i] = strings.TrimLeft(line, " \t")
			continue
		}
		lines[i] = line[indent:]
	}
	return strings.Join(lines, "\n")
}

func (r *reader) str() (Value, error) {
	var sb strings.Builder
	for {
		if r.eof() {
			return Value{}, ErrIncomplete
		}
		c := r.next()
		switch c {
		case '"':
			return Str(sb.String()), nil
		case '\\':
			if r.eof() {
				return Value{}, ErrIncomplete
			}
			e := r.next()
			switch e {
			case '"', '\\':
				sb.WriteRune(e)
			case 'n':
				sb.WriteByte('\n')
			case 't':
				sb.WriteByte('\t')
			case 'r':
				sb.WriteByte('\r')
			case 'x', 'u', 'U':
				n := map[rune]int{'x': 2, 'u': 4, 'U': 8}[e]
				code, err := r.hex(n)
				if err != nil {
					return Value{}, err
				}
				if e == 'x' {
					sb.WriteByte(byte(code)) // a raw byte, so invalid UTF-8 round-trips
				} else {
					sb.WriteRune(rune(code))
				}
			default:
				return Value{}, r.errf("unknown escape \\%c", e)
			}
		default:
			sb.WriteRune(c)
		}
	}
}

// hex reads exactly n hex digits.
func (r *reader) hex(n int) (uint32, error) {
	var code uint32
	for i := 0; i < n; i++ {
		if r.eof() {
			return 0, ErrIncomplete
		}
		c := r.next()
		var d uint32
		switch {
		case c >= '0' && c <= '9':
			d = uint32(c - '0')
		case c >= 'a' && c <= 'f':
			d = uint32(c-'a') + 10
		case c >= 'A' && c <= 'F':
			d = uint32(c-'A') + 10
		default:
			return 0, r.errf("bad hex digit %q in escape", c)
		}
		code = code<<4 | d
	}
	return code, nil
}

func (r *reader) atom() (Value, error) {
	start := r.pos
	for !r.eof() && !isDelimiter(r.peek()) {
		r.next()
	}
	tok := r.src[start:r.pos]
	if tok == "" {
		return Value{}, r.errf("unexpected character %q", r.peek())
	}
	switch tok {
	case "#t":
		return True, nil
	case "#f":
		return False, nil
	case ".":
		return Value{}, r.errf("unexpected .")
	}
	if strings.HasPrefix(tok, "#") {
		return Value{}, r.errf("unknown # syntax %q", tok)
	}
	if n, err := strconv.ParseInt(tok, 10, 64); err == nil {
		return Int(n), nil
	}
	if f, err := strconv.ParseFloat(tok, 64); err == nil {
		// Reject things ParseFloat accepts that are not numbers to a Scheme
		// reader, like "inf", "nan", "0x1p3" and underscores.
		if isPlainFloat(tok) {
			return Float(f), nil
		}
	}
	return Sym(tok), nil
}

func isPlainFloat(tok string) bool {
	for _, c := range tok {
		switch {
		case c >= '0' && c <= '9', c == '.', c == '-', c == '+', c == 'e', c == 'E':
		default:
			return false
		}
	}
	return strings.ContainsAny(tok, "0123456789")
}
