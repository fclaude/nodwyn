package scheme

import (
	"fmt"
	"reflect"
	"regexp"
)

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
		// Everything a provider or the first call would reject is refused
		// here, so init.scm fails at the line that is wrong.
		if err := checkToolShape(c, name, proc, meta); err != nil {
			return Value{}, err
		}
		p := &Prim{
			Name: name,
			Meta: meta,
			Handler: func(call *Call) (Value, error) {
				return call.In.Apply(call.Ctx, proc, call.Args)
			},
		}
		if c.In.reloading {
			// The tool list is fixed for the session: a tool keeps its
			// schema and takes its new body; anything else waits.
			if old, ok := c.In.prims[name]; ok && old.Meta.Origin == "scheme" && reflect.DeepEqual(old.Meta, meta) {
				old.Handler = p.Handler
			} else {
				c.In.noteReload("tool %s is new, or its description or parameters changed: the tool list is fixed for this session, so it applies from the next one", name)
			}
			return Void, nil
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
			if c.In.reloading {
				c.In.noteReload("register-check: the guard's checks are fixed for this session; changes apply from the next one")
				return Void, nil
			}
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

// toolNameOK is the name a provider accepts for a tool.
var toolNameOK = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// checkToolShape refuses a tool whose name a provider would reject, whose
// parameters share a name (ArgsFromJSON would then fail every call), or
// whose procedure cannot take the arguments its parameters promise.
func checkToolShape(c *Call, name string, proc Value, meta Meta) error {
	if !toolNameOK.MatchString(name) {
		return errorf(c.Name, "tool name %q must be 1 to 64 letters, digits, - or _", name)
	}
	names := map[string]bool{}
	for _, pa := range meta.Params {
		if names[pa.Name] {
			return errorf(c.Name, "parameter %q is listed twice", pa.Name)
		}
		names[pa.Name] = true
	}
	// A primitive checks its own arity at the call; a closure is checked
	// here against what a call can pass: all params, or more when variadic.
	if cl := proc.Proc; cl != nil {
		n := len(meta.Params)
		if len(cl.Params) > n || (len(cl.Params) < n && cl.Rest == "") || (meta.Variadic && cl.Rest == "") {
			takes := fmt.Sprintf("%d arguments", len(cl.Params))
			if cl.Rest != "" {
				takes = fmt.Sprintf("%d or more arguments", len(cl.Params))
			}
			return errorf(c.Name, "the procedure takes %s, which does not fit the %d parameters listed (variadic: %t)", takes, n, meta.Variadic)
		}
	}
	return nil
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
