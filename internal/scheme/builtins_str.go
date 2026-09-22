package scheme

import (
	"strings"
	"unicode/utf8"
)

func (in *Interp) registerStrings() {
	s1 := []Param{{Name: "s", Type: "string"}}
	any1 := []Param{{Name: "x", Type: "any"}}

	in.mustRegisterOpaque("string?", "True for strings.", any1, false, func(c *Call) (Value, error) {
		return Bool(c.Args[0].Kind == KString), nil
	})
	in.mustRegisterOpaque("string-length", "Length in runes.", s1, false, func(c *Call) (Value, error) {
		s, err := c.String(0)
		if err != nil {
			return Value{}, err
		}
		return Int(int64(utf8.RuneCountInString(s))), nil
	})
	in.mustRegister("string-append", "Concatenate strings.", nil, true, func(c *Call) (Value, error) {
		var sb strings.Builder
		for i := range c.Args {
			s, err := c.String(i)
			if err != nil {
				return Value{}, err
			}
			sb.WriteString(s)
		}
		return Str(sb.String()), nil
	})
	in.mustRegister("substring", "Runes from start to end (exclusive); end defaults to the length.", []Param{{Name: "s", Type: "string"}, {Name: "start", Type: "integer"}}, true, func(c *Call) (Value, error) {
		s, err := c.String(0)
		if err != nil {
			return Value{}, err
		}
		start, err := c.Int(1)
		if err != nil {
			return Value{}, err
		}
		runes := []rune(s)
		end := int64(len(runes))
		if len(c.Args) > 2 {
			if end, err = c.Int(2); err != nil {
				return Value{}, err
			}
		}
		if len(c.Args) > 3 {
			return Value{}, errorf(c.Name, "expected 2 or 3 arguments, got %d", len(c.Args))
		}
		if start < 0 || end > int64(len(runes)) || start > end {
			return Value{}, errorf(c.Name, "range %d..%d out of bounds for length %d", start, end, len(runes))
		}
		return Str(string(runes[start:end])), nil
	})
	strCmp := func(name, doc string, ok func(a, b string) bool) {
		in.mustRegisterOpaque(name, doc, []Param{{Name: "a", Type: "string"}, {Name: "b", Type: "string"}}, true, func(c *Call) (Value, error) {
			for i := 0; i+1 < len(c.Args); i++ {
				a, err := c.String(i)
				if err != nil {
					return Value{}, err
				}
				b, err := c.String(i + 1)
				if err != nil {
					return Value{}, err
				}
				if !ok(a, b) {
					return False, nil
				}
			}
			return True, nil
		})
	}
	strCmp("string=?", "String equality chain.", func(a, b string) bool { return a == b })
	strCmp("string<?", "Lexicographic ordering chain.", func(a, b string) bool { return a < b })
	mapStr := func(name, doc string, f func(string) string) {
		in.mustRegister(name, doc, s1, false, func(c *Call) (Value, error) {
			s, err := c.String(0)
			if err != nil {
				return Value{}, err
			}
			return Str(f(s)), nil
		})
	}
	mapStr("string-upcase", "Upper-case the string.", strings.ToUpper)
	mapStr("string-downcase", "Lower-case the string.", strings.ToLower)
	mapStr("string-trim", "Strip leading and trailing whitespace.", strings.TrimSpace)

	in.mustRegister("string-split", "Split s on a literal separator.", []Param{{Name: "s", Type: "string"}, {Name: "sep", Type: "string"}}, false, func(c *Call) (Value, error) {
		s, err := c.String(0)
		if err != nil {
			return Value{}, err
		}
		sep, err := c.String(1)
		if err != nil {
			return Value{}, err
		}
		if sep == "" {
			return Value{}, errorf(c.Name, "separator must not be empty")
		}
		parts := strings.Split(s, sep)
		out := make([]Value, len(parts))
		for i, p := range parts {
			out[i] = Str(p)
		}
		return List(out...), nil
	})
	in.mustRegister("string-join", "Join a list of strings with a separator.", []Param{{Name: "l", Type: "list"}, {Name: "sep", Type: "string"}}, false, func(c *Call) (Value, error) {
		items, err := c.List(0)
		if err != nil {
			return Value{}, err
		}
		sep, err := c.String(1)
		if err != nil {
			return Value{}, err
		}
		parts := make([]string, len(items))
		for i, v := range items {
			if v.Kind != KString {
				return Value{}, errorf(c.Name, "element %d is not a string: %s", i, Write(v))
			}
			parts[i] = v.S
		}
		return Str(strings.Join(parts, sep)), nil
	})
	two := func(name, doc, a, b string, f func(x, y string) bool) {
		in.mustRegisterOpaque(name, doc, []Param{{Name: a, Type: "string"}, {Name: b, Type: "string"}}, false, func(c *Call) (Value, error) {
			x, err := c.String(0)
			if err != nil {
				return Value{}, err
			}
			y, err := c.String(1)
			if err != nil {
				return Value{}, err
			}
			return Bool(f(x, y)), nil
		})
	}
	two("string-contains?", "True if sub occurs in s.", "s", "sub", strings.Contains)
	two("string-prefix?", "True if s starts with prefix.", "prefix", "s", func(p, s string) bool { return strings.HasPrefix(s, p) })
	two("string-suffix?", "True if s ends with suffix.", "suffix", "s", func(p, s string) bool { return strings.HasSuffix(s, p) })
	in.mustRegister("string-replace", "Replace every occurrence of old with new.", []Param{{Name: "s", Type: "string"}, {Name: "old", Type: "string"}, {Name: "new", Type: "string"}}, false, func(c *Call) (Value, error) {
		s, err := c.String(0)
		if err != nil {
			return Value{}, err
		}
		old, err := c.String(1)
		if err != nil {
			return Value{}, err
		}
		nw, err := c.String(2)
		if err != nil {
			return Value{}, err
		}
		return Str(strings.ReplaceAll(s, old, nw)), nil
	})
	in.mustRegister("string->symbol", "The symbol with this name.", s1, false, func(c *Call) (Value, error) {
		s, err := c.String(0)
		if err != nil {
			return Value{}, err
		}
		return Sym(s), nil
	})
	in.mustRegister("symbol->string", "The symbol's name.", []Param{{Name: "sym", Type: "any"}}, false, func(c *Call) (Value, error) {
		s, err := c.Symbol(0)
		if err != nil {
			return Value{}, err
		}
		return Str(s), nil
	})
	in.mustRegisterOpaque("symbol?", "True for symbols.", any1, false, func(c *Call) (Value, error) {
		return Bool(c.Args[0].Kind == KSymbol), nil
	})
	in.mustRegister("format", "Format with ~a (display), ~s (write), ~% (newline), ~~ (tilde).", []Param{{Name: "fmt", Type: "string"}}, true, func(c *Call) (Value, error) {
		f, err := c.String(0)
		if err != nil {
			return Value{}, err
		}
		var sb strings.Builder
		next := 1
		for i := 0; i < len(f); i++ {
			if f[i] != '~' {
				sb.WriteByte(f[i])
				continue
			}
			i++
			if i >= len(f) {
				return Value{}, errorf(c.Name, "dangling ~")
			}
			switch f[i] {
			case 'a', 's':
				if next >= len(c.Args) {
					return Value{}, errorf(c.Name, "too few arguments for %q", f)
				}
				if f[i] == 'a' {
					sb.WriteString(Display(c.Args[next]))
				} else {
					sb.WriteString(Write(c.Args[next]))
				}
				next++
			case '%':
				sb.WriteByte('\n')
			case '~':
				sb.WriteByte('~')
			default:
				return Value{}, errorf(c.Name, "unknown directive ~%c", f[i])
			}
		}
		if next != len(c.Args) {
			return Value{}, errorf(c.Name, "too many arguments for %q", f)
		}
		return Str(sb.String()), nil
	})
}
