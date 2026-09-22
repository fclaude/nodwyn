package scheme

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
)

// mustRegister registers a builtin; a bad declaration is a programming
// error, so it panics at startup rather than failing later.
func (in *Interp) mustRegister(name, doc string, params []Param, variadic bool, h func(c *Call) (Value, error)) {
	err := in.Register(&Prim{
		Name:    name,
		Meta:    Meta{Doc: doc, Params: params, Variadic: variadic, Origin: "builtin", TaintOpaque: taintOpaque[name]},
		Handler: h,
	})
	if err != nil {
		panic(err)
	}
}

// taintOpaque lists the builtins whose result is a *measurement* of their
// arguments rather than something made of them: a length, a predicate, a
// comparison. Their results carry no provenance (L2, Phase 6). Everything
// else propagates, which is the safe default.
//
// Accepted limit (SPEC Risk 1): a value can still be reconstructed one bit
// at a time through these. L2 is scoped to the model's unintended leak, not
// to a determined extractor; the egress lines are binding and redaction.
var taintOpaque = map[string]bool{
	"length": true, "string-length": true, "vector-length": true,
	"null?": true, "pair?": true, "list?": true, "string?": true, "symbol?": true,
	"boolean?": true, "number?": true, "integer?": true, "float?": true,
	"procedure?": true, "handle?": true, "vector?": true, "tainted?": true,
	"zero?": true, "positive?": true, "negative?": true, "even?": true, "odd?": true,
	"eq?": true, "eqv?": true, "equal?": true, "not": true,
	"=": true, "<": true, ">": true, "<=": true, ">=": true,
	"string=?": true, "string<?": true,
	"string-contains?": true, "string-prefix?": true, "string-suffix?": true,
	"taint-of": true,
}

func (in *Interp) registerBuiltins() {
	in.registerNumbers()
	in.registerLists()
	in.registerStrings()
	in.registerVectors()
	in.registerCore()
	in.registerJSON()
	in.registerMeta()
	in.registerTaint()
}

func (in *Interp) registerCore() {
	any1 := []Param{{Name: "x", Type: "any"}}
	any2 := []Param{{Name: "a", Type: "any"}, {Name: "b", Type: "any"}}

	in.mustRegister("not", "True only for #f.", any1, false, func(c *Call) (Value, error) {
		return Bool(!c.Args[0].IsTrue()), nil
	})
	in.mustRegister("boolean?", "True for #t and #f.", any1, false, func(c *Call) (Value, error) {
		return Bool(c.Args[0].Kind == KBool), nil
	})
	in.mustRegister("eq?", "Identity for pairs and procedures; value for everything else.", any2, false, func(c *Call) (Value, error) {
		return Bool(Eqv(c.Args[0], c.Args[1])), nil
	})
	in.mustRegister("eqv?", "Alias of eq?.", any2, false, func(c *Call) (Value, error) {
		return Bool(Eqv(c.Args[0], c.Args[1])), nil
	})
	in.mustRegister("equal?", "Structural equality.", any2, false, func(c *Call) (Value, error) {
		return Bool(Equal(c.Args[0], c.Args[1])), nil
	})
	in.mustRegister("procedure?", "True for closures and primitives.", any1, false, func(c *Call) (Value, error) {
		return Bool(c.Args[0].IsProcedure()), nil
	})
	in.mustRegister("handle?", "True for credential handles.", any1, false, func(c *Call) (Value, error) {
		return Bool(c.Args[0].Kind == KHandle), nil
	})
	in.mustRegister("void", "The void value.", nil, false, func(c *Call) (Value, error) {
		return Void, nil
	})
	in.mustRegister("error", "Raise an error with a message and irritants.", []Param{{Name: "message", Type: "string"}}, true, func(c *Call) (Value, error) {
		msg, err := c.String(0)
		if err != nil {
			return Value{}, err
		}
		return Value{}, &Error{Message: msg, Irritants: c.Args[1:]}
	})

	in.mustRegister("display", "Print x in human form.", any1, false, func(c *Call) (Value, error) {
		_, err := io.WriteString(c.In.out, Display(c.Args[0]))
		return Void, err
	})
	in.mustRegister("write", "Print x in machine form.", any1, false, func(c *Call) (Value, error) {
		_, err := io.WriteString(c.In.out, Write(c.Args[0]))
		return Void, err
	})
	in.mustRegister("newline", "Print a newline.", nil, false, func(c *Call) (Value, error) {
		_, err := io.WriteString(c.In.out, "\n")
		return Void, err
	})
}

// --- JSON -----------------------------------------------------------------

func (in *Interp) registerJSON() {
	in.mustRegister("json-parse", "Parse JSON text into a value (objects become alists with string keys).", []Param{{Name: "s", Type: "string"}}, false, func(c *Call) (Value, error) {
		s, err := c.String(0)
		if err != nil {
			return Value{}, err
		}
		dec := json.NewDecoder(strings.NewReader(s))
		dec.UseNumber()
		v, err := jsonDecode(dec)
		if err != nil {
			return Value{}, errorf(c.Name, "%v", err)
		}
		if _, err := dec.Token(); err != io.EOF {
			return Value{}, errorf(c.Name, "trailing data after JSON value")
		}
		return v, nil
	})
	in.mustRegister("json-emit", "Render a value as compact JSON text.", []Param{{Name: "v", Type: "any"}}, false, func(c *Call) (Value, error) {
		var buf bytes.Buffer
		if err := jsonEncode(&buf, c.Args[0]); err != nil {
			return Value{}, errorf(c.Name, "%v", err)
		}
		return Str(buf.String()), nil
	})
}

func jsonDecode(dec *json.Decoder) (Value, error) {
	tok, err := dec.Token()
	if err != nil {
		return Value{}, err
	}
	return jsonFromToken(dec, tok)
}

func jsonFromToken(dec *json.Decoder, tok json.Token) (Value, error) {
	switch t := tok.(type) {
	case nil:
		return Sym("null"), nil
	case bool:
		return Bool(t), nil
	case string:
		return Str(t), nil
	case json.Number:
		if i, err := t.Int64(); err == nil {
			return Int(i), nil
		}
		f, err := t.Float64()
		if err != nil {
			return Value{}, err
		}
		return Float(f), nil
	case json.Delim:
		switch t {
		case '[':
			var items []Value
			for dec.More() {
				v, err := jsonDecode(dec)
				if err != nil {
					return Value{}, err
				}
				items = append(items, v)
			}
			if _, err := dec.Token(); err != nil { // ]
				return Value{}, err
			}
			return List(items...), nil
		case '{':
			var items []Value
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return Value{}, err
				}
				key, ok := kt.(string)
				if !ok {
					return Value{}, fmt.Errorf("object key is not a string")
				}
				v, err := jsonDecode(dec)
				if err != nil {
					return Value{}, err
				}
				items = append(items, Cons(Str(key), v))
			}
			if _, err := dec.Token(); err != nil { // }
				return Value{}, err
			}
			return List(items...), nil
		}
	}
	return Value{}, fmt.Errorf("unexpected token %v", tok)
}

func jsonEncode(buf *bytes.Buffer, v Value) error {
	switch v.Kind {
	case KNil:
		buf.WriteString("[]")
	case KBool:
		if v.I != 0 {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
	case KInt:
		buf.WriteString(strconv.FormatInt(v.I, 10))
	case KFloat:
		if math.IsNaN(v.F) || math.IsInf(v.F, 0) {
			return fmt.Errorf("cannot emit %s", Write(v))
		}
		buf.WriteString(strconv.FormatFloat(v.F, 'g', -1, 64))
	case KString:
		b, _ := json.Marshal(v.S)
		buf.Write(b)
	case KSymbol:
		if v.S != "null" {
			return fmt.Errorf("cannot emit symbol %s", v.S)
		}
		buf.WriteString("null")
	case KPair:
		items, ok := v.Slice()
		if !ok {
			return fmt.Errorf("cannot emit improper list %s", Write(v))
		}
		if isAlist(items) {
			buf.WriteByte('{')
			for i, it := range items {
				if i > 0 {
					buf.WriteByte(',')
				}
				b, _ := json.Marshal(it.P.Car.S)
				buf.Write(b)
				buf.WriteByte(':')
				if err := jsonEncode(buf, it.P.Cdr); err != nil {
					return err
				}
			}
			buf.WriteByte('}')
			return nil
		}
		buf.WriteByte('[')
		for i, it := range items {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := jsonEncode(buf, it); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	default:
		return fmt.Errorf("cannot emit %s", Write(v))
	}
	return nil
}

func isAlist(items []Value) bool {
	if len(items) == 0 {
		return false
	}
	for _, it := range items {
		if it.Kind != KPair || it.P.Car.Kind != KString {
			return false
		}
	}
	return true
}

// --- Meta -----------------------------------------------------------------

func (in *Interp) registerMeta() {
	in.mustRegister("primitives", "Sorted list of primitive names.", nil, false, func(c *Call) (Value, error) {
		ps := c.In.Prims()
		out := make([]Value, len(ps))
		for i, p := range ps {
			out[i] = Str(p.Name)
		}
		return List(out...), nil
	})
	in.mustRegister("primitive-info", "Metadata alist for a primitive: doc, params, variadic, egress, irreversible, host, origin.", []Param{{Name: "name", Type: "string"}}, false, func(c *Call) (Value, error) {
		name, err := c.String(0)
		if err != nil {
			return Value{}, err
		}
		p, ok := c.In.Lookup(name)
		if !ok {
			return Value{}, errorf(c.Name, "no primitive named %q", name)
		}
		params := make([]Value, len(p.Meta.Params))
		for i, pa := range p.Meta.Params {
			params[i] = List(Str(pa.Name), Str(pa.Type), Str(pa.Doc))
		}
		return List(
			Cons(Sym("doc"), Str(p.Meta.Doc)),
			Cons(Sym("params"), List(params...)),
			Cons(Sym("variadic"), Bool(p.Meta.Variadic)),
			Cons(Sym("egress"), Bool(p.Meta.Egress)),
			Cons(Sym("irreversible"), Bool(p.Meta.Irreversible)),
			Cons(Sym("host"), Bool(p.Meta.Host)),
			Cons(Sym("origin"), Str(p.Meta.Origin)),
		), nil
	})
	in.mustRegister("register-tool", "Register a primitive backed by a Scheme procedure; meta is an alist with doc and params (see docs/scheme.md §7).", []Param{{Name: "name", Type: "string"}, {Name: "proc", Type: "any"}, {Name: "meta", Type: "alist"}}, false, func(c *Call) (Value, error) {
		name, err := c.String(0)
		if err != nil {
			return Value{}, err
		}
		proc, err := c.Proc(1)
		if err != nil {
			return Value{}, err
		}
		meta, err := parseToolMeta(c, c.Args[2])
		if err != nil {
			return Value{}, err
		}
		p := &Prim{
			Name: name,
			Meta: meta,
			Handler: func(call *Call) (Value, error) {
				return call.In.Apply(call.Ctx, proc, call.Args)
			},
		}
		if err := c.In.Register(p); err != nil {
			return Value{}, errorf(c.Name, "%v", err)
		}
		return Void, nil
	})
	in.registerChecker()
	if err := in.Register(&Prim{
		Name: "load",
		Meta: Meta{
			Doc:    "Evaluate a Scheme file in the global environment.",
			Params: []Param{{Name: "path", Type: "string", Doc: "file to load", Role: "path"}},
			Host:   true,
			Hidden: true,
			Origin: "builtin",
		},
		Handler: func(c *Call) (Value, error) {
			path, err := c.String(0)
			if err != nil {
				return Value{}, err
			}
			if err := c.In.LoadFile(c.Ctx, path); err != nil {
				return Value{}, err
			}
			return Void, nil
		},
	}); err != nil {
		panic(err)
	}
}

// registerChecker registers the register-check builtin (ADR 20, Phase 5).
// The builtin is hidden: the model cannot call it. It is restricted to
// user-originated Scheme (OriginUser) so the model cannot add checkers at
// runtime. Verdict is "deny" or "confirm" — never "allow" — so the
// strictest-wins rule in the guard guarantees a user checker can only
// tighten, never relax.
func (in *Interp) registerChecker() {
	p := &Prim{
		Name: "register-check",
		Meta: Meta{
			Doc: `Add a Scheme-written guard checker (register-check, ADR 20).
(name verdict predicate)
  name       string — the checker name logged in guard.decision
  verdict    "deny" or "confirm" — the verdict when predicate is #t
  predicate  a procedure (prim-name args) → boolean
The checker is added after all Go checkers and can only tighten.
This builtin is hidden from the model and callable only from
user-originated Scheme.`,
			Params: []Param{{Name: "name", Type: "string"}, {Name: "verdict", Type: "string"}, {Name: "predicate", Type: "any"}},
			Hidden: true,
			Origin: "builtin",
		},
		Handler: func(c *Call) (Value, error) {
			if OriginFrom(c.Ctx) != OriginUser {
				return Value{}, errorf(c.Name, "register-check is restricted to user-originated Scheme")
			}
			name, err := c.String(0)
			if err != nil {
				return Value{}, err
			}
			verdict, err := c.String(1)
			if err != nil {
				return Value{}, err
			}
			pred, err := c.Proc(2)
			if err != nil {
				return Value{}, err
			}
			if c.In.gate == nil {
				return Value{}, errorf(c.Name, "no gate installed; register-check needs a guard")
			}
			if err := c.In.gate.RegisterChecker(name, verdict, pred); err != nil {
				return Value{}, errorf(c.Name, "%v", err)
			}
			return Void, nil
		},
	}
	if err := in.Register(p); err != nil {
		panic(err)
	}
}

// parseToolMeta reads the register-tool meta alist.
func parseToolMeta(c *Call, v Value) (Meta, error) {
	items, ok := v.Slice()
	if !ok {
		return Meta{}, errorf(c.Name, "meta must be an alist")
	}
	m := Meta{Origin: "scheme"}
	seen := map[string]bool{}
	for _, it := range items {
		if it.Kind != KPair || it.P.Car.Kind != KSymbol {
			return Meta{}, errorf(c.Name, "bad meta entry %s", Write(it))
		}
		key, val := it.P.Car.S, it.P.Cdr
		seen[key] = true
		switch key {
		case "doc":
			if val.Kind != KString {
				return Meta{}, errorf(c.Name, "doc must be a string")
			}
			m.Doc = val.S
		case "params":
			ps, ok := val.Slice()
			if !ok {
				return Meta{}, errorf(c.Name, "params must be a list of (name type doc)")
			}
			for _, p := range ps {
				parts, ok := p.Slice()
				if !ok || len(parts) != 3 || parts[0].Kind != KString || parts[1].Kind != KString || parts[2].Kind != KString {
					return Meta{}, errorf(c.Name, "bad param %s; expected (\"name\" \"type\" \"doc\")", Write(p))
				}
				switch parts[1].S {
				case "string", "integer", "number", "boolean", "list", "alist", "vector", "any":
				default:
					return Meta{}, errorf(c.Name, "unknown param type %q", parts[1].S)
				}
				m.Params = append(m.Params, Param{Name: parts[0].S, Type: parts[1].S, Doc: parts[2].S})
			}
		case "variadic", "egress", "irreversible":
			if val.Kind != KBool {
				return Meta{}, errorf(c.Name, "%s must be #t or #f", key)
			}
			switch key {
			case "variadic":
				m.Variadic = val.IsTrue()
			case "egress":
				m.Egress = val.IsTrue()
			case "irreversible":
				m.Irreversible = val.IsTrue()
			}
		default:
			return Meta{}, errorf(c.Name, "unknown meta key %s", key)
		}
	}
	if !seen["doc"] || !seen["params"] {
		return Meta{}, errorf(c.Name, "doc and params are required")
	}
	return m, nil
}
