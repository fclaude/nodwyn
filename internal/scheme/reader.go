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
	src       string
	pos       int
	line, col int
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

func isDelimiter(c rune) bool {
	return c == 0 || unicode.IsSpace(c) || c == '(' || c == ')' || c == '"' || c == ';' || c == '\''
}

func (r *reader) datum() (Value, error) {
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
		r.next()
		return r.str()
	}
	return r.atom()
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
