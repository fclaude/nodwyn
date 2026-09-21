package scheme

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sort"
	"strings"
)

//go:embed all:prelude
var preludeFS embed.FS

// kernelVersion changes only when eval/apply semantics change. It is part
// of the fingerprint so a semantic change is visible as a new kernel.
const kernelVersion = "b1"

// specialForms is the fixed set of reserved names. There is no API to add
// to it; the fingerprint covers it.
var specialForms = []string{
	"define", "lambda", "if", "let", "let*", "begin", "set!",
	"quote", "quasiquote", "unquote", "unquote-splicing",
	"cond", "else", "=>", "and", "or",
}

// Interp is one interpreter instance: a global environment, the primitive
// table, and the guard hook.
type Interp struct {
	global *Env
	prims  map[string]*Prim
	gate   Gate
	out    io.Writer
	isSF   map[string]bool
}

// Option configures New.
type Option func(*Interp)

// WithOutput sets where display/write/newline go. Default: io.Discard —
// the host decides who sees output (ADR 10).
func WithOutput(w io.Writer) Option { return func(in *Interp) { in.out = w } }

// New creates an interpreter with the builtins and the prelude loaded.
func New(opts ...Option) *Interp {
	in := &Interp{
		global: NewEnv(nil),
		prims:  make(map[string]*Prim),
		out:    io.Discard,
		isSF:   make(map[string]bool, len(specialForms)),
	}
	for _, s := range specialForms {
		in.isSF[s] = true
	}
	for _, o := range opts {
		o(in)
	}
	in.registerBuiltins()
	if err := in.loadPrelude(); err != nil {
		panic("scheme: prelude: " + err.Error())
	}
	return in
}

func (in *Interp) loadPrelude() error {
	entries, err := fs.ReadDir(preludeFS, "prelude")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".scm") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, n := range names {
		src, err := preludeFS.ReadFile("prelude/" + n)
		if err != nil {
			return err
		}
		if _, err := in.EvalString(context.Background(), string(src)); err != nil {
			return fmt.Errorf("%s: %w", n, err)
		}
	}
	return nil
}

// Global returns the global environment.
func (in *Interp) Global() *Env { return in.global }

// Output returns the interpreter's output writer.
func (in *Interp) Output() io.Writer { return in.out }

// Fingerprint identifies the kernel: its version and its special forms.
// Loading Scheme code cannot change it; the test suite checks that.
func (in *Interp) Fingerprint() string {
	h := sha256.New()
	h.Write([]byte("kernel=" + kernelVersion + "\n"))
	names := make([]string, 0, len(in.isSF))
	for s := range in.isSF {
		names = append(names, s)
	}
	sort.Strings(names)
	h.Write([]byte(strings.Join(names, ",")))
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// EvalString reads and evaluates every form in src in the global
// environment and returns the last value (Void for empty input).
func (in *Interp) EvalString(ctx context.Context, src string) (Value, error) {
	forms, err := Read(src)
	if err != nil {
		return Value{}, err
	}
	result := Void
	for _, f := range forms {
		result, err = in.Eval(ctx, f, in.global)
		if err != nil {
			return Value{}, err
		}
	}
	return result, nil
}

// LoadFile evaluates a file's forms in the global environment.
func (in *Interp) LoadFile(ctx context.Context, path string) error {
	src, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if _, err := in.EvalString(ctx, string(src)); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// Apply calls a procedure with already-evaluated arguments.
func (in *Interp) Apply(ctx context.Context, f Value, args []Value) (Value, error) {
	switch f.Kind {
	case KPrim:
		return in.applyPrim(ctx, f.Prim, args)
	case KProc:
		env, err := bindParams(f.Proc, args)
		if err != nil {
			return Value{}, err
		}
		return in.evalBody(ctx, f.Proc.Body, env)
	default:
		return Value{}, errorf("apply", "not a procedure: %s", Write(f))
	}
}

// evalBody evaluates a sequence, the last expression in tail position of
// this Go frame.
func (in *Interp) evalBody(ctx context.Context, body []Value, env *Env) (Value, error) {
	for i := 0; i < len(body)-1; i++ {
		if _, err := in.Eval(ctx, body[i], env); err != nil {
			return Value{}, err
		}
	}
	return in.Eval(ctx, body[len(body)-1], env)
}

func bindParams(c *Closure, args []Value) (*Env, error) {
	env := NewEnv(c.Env)
	n := len(c.Params)
	if len(args) < n || (len(args) > n && c.Rest == "") {
		name := c.Name
		if name == "" {
			name = "#<procedure>"
		}
		if c.Rest != "" {
			return nil, errorf(name, "expected at least %d argument(s), got %d", n, len(args))
		}
		return nil, errorf(name, "expected %d argument(s), got %d", n, len(args))
	}
	for i, p := range c.Params {
		env.Define(p, args[i])
	}
	if c.Rest != "" {
		env.Define(c.Rest, List(args[n:]...))
	}
	return env, nil
}

// Eval evaluates x in env. Tail positions loop instead of recursing, so
// tail-recursive Scheme runs in constant Go stack (docs/scheme.md §3).
func (in *Interp) Eval(ctx context.Context, x Value, env *Env) (Value, error) {
	for {
		switch x.Kind {
		case KSymbol:
			v, ok := env.Lookup(x.S)
			if !ok {
				return Value{}, errorf("eval", "unbound variable: %s", x.S)
			}
			return v, nil
		case KNil:
			return Value{}, errorf("eval", "empty application ()")
		case KPair:
			// fallthrough to the form/application logic below
		default:
			return x, nil // self-evaluating
		}

		head := x.P.Car
		if head.Kind == KSymbol && in.isSF[head.S] {
			form, err := formArgs(x)
			if err != nil {
				return Value{}, err
			}
			switch head.S {
			case "quote":
				if len(form) != 1 {
					return Value{}, errorf("quote", "expected 1 argument")
				}
				return form[0], nil

			case "quasiquote":
				if len(form) != 1 {
					return Value{}, errorf("quasiquote", "expected 1 argument")
				}
				return in.quasi(ctx, form[0], env)

			case "unquote", "unquote-splicing":
				return Value{}, errorf(head.S, "outside quasiquote")

			case "else", "=>":
				return Value{}, errorf(head.S, "outside cond")

			case "if":
				if len(form) < 2 || len(form) > 3 {
					return Value{}, errorf("if", "expected 2 or 3 arguments")
				}
				t, err := in.Eval(ctx, form[0], env)
				if err != nil {
					return Value{}, err
				}
				if t.IsTrue() {
					x = form[1]
				} else if len(form) == 3 {
					x = form[2]
				} else {
					return Void, nil
				}
				continue

			case "define":
				return in.evalDefine(ctx, form, env)

			case "set!":
				if len(form) != 2 || form[0].Kind != KSymbol {
					return Value{}, errorf("set!", "expected (set! name expr)")
				}
				v, err := in.Eval(ctx, form[1], env)
				if err != nil {
					return Value{}, err
				}
				if !env.Set(form[0].S, v) {
					return Value{}, errorf("set!", "unbound variable: %s", form[0].S)
				}
				return Void, nil

			case "lambda":
				c, err := makeClosure("lambda", form, env)
				if err != nil {
					return Value{}, err
				}
				return Value{Kind: KProc, Proc: c}, nil

			case "begin":
				if len(form) == 0 {
					return Void, nil
				}
				for i := 0; i < len(form)-1; i++ {
					if _, err := in.Eval(ctx, form[i], env); err != nil {
						return Value{}, err
					}
				}
				x = form[len(form)-1]
				continue

			case "let", "let*":
				nx, nenv, err := in.evalLet(ctx, head.S, form, env)
				if err != nil {
					return Value{}, err
				}
				x, env = nx, nenv
				continue

			case "cond":
				nx, done, v, err := in.evalCond(ctx, form, env)
				if err != nil {
					return Value{}, err
				}
				if done {
					return v, nil
				}
				x = nx
				continue

			case "and":
				if len(form) == 0 {
					return True, nil
				}
				for i := 0; i < len(form)-1; i++ {
					v, err := in.Eval(ctx, form[i], env)
					if err != nil {
						return Value{}, err
					}
					if !v.IsTrue() {
						return v, nil
					}
				}
				x = form[len(form)-1]
				continue

			case "or":
				if len(form) == 0 {
					return False, nil
				}
				for i := 0; i < len(form)-1; i++ {
					v, err := in.Eval(ctx, form[i], env)
					if err != nil {
						return Value{}, err
					}
					if v.IsTrue() {
						return v, nil
					}
				}
				x = form[len(form)-1]
				continue
			}
		}

		// Application.
		if err := ctx.Err(); err != nil {
			return Value{}, wrap("eval", err)
		}
		f, err := in.Eval(ctx, head, env)
		if err != nil {
			return Value{}, err
		}
		argForms, ok := x.P.Cdr.Slice()
		if !ok {
			return Value{}, errorf("eval", "improper application %s", Write(x))
		}
		args := make([]Value, len(argForms))
		for i, a := range argForms {
			if args[i], err = in.Eval(ctx, a, env); err != nil {
				return Value{}, err
			}
		}
		switch f.Kind {
		case KPrim:
			return in.applyPrim(ctx, f.Prim, args)
		case KProc:
			nenv, err := bindParams(f.Proc, args)
			if err != nil {
				return Value{}, err
			}
			body := f.Proc.Body
			for i := 0; i < len(body)-1; i++ {
				if _, err := in.Eval(ctx, body[i], nenv); err != nil {
					return Value{}, err
				}
			}
			x, env = body[len(body)-1], nenv
			continue
		default:
			return Value{}, errorf("eval", "not a procedure: %s", Write(f))
		}
	}
}

func formArgs(x Value) ([]Value, error) {
	args, ok := x.P.Cdr.Slice()
	if !ok {
		return nil, errorf(Write(x.P.Car), "improper form %s", Write(x))
	}
	return args, nil
}

func (in *Interp) evalDefine(ctx context.Context, form []Value, env *Env) (Value, error) {
	if len(form) < 2 {
		return Value{}, errorf("define", "expected (define name expr) or (define (name params...) body...)")
	}
	target := form[0]
	switch target.Kind {
	case KSymbol:
		if len(form) != 2 {
			return Value{}, errorf("define", "expected exactly one expression for %s", target.S)
		}
		v, err := in.Eval(ctx, form[1], env)
		if err != nil {
			return Value{}, err
		}
		if v.Kind == KProc && v.Proc.Name == "" {
			v.Proc.Name = target.S
		}
		env.Define(target.S, v)
		return Void, nil
	case KPair:
		name := target.P.Car
		if name.Kind != KSymbol {
			return Value{}, errorf("define", "procedure name must be a symbol, got %s", Write(name))
		}
		lambdaForm := append([]Value{target.P.Cdr}, form[1:]...)
		c, err := makeClosure(name.S, lambdaForm, env)
		if err != nil {
			return Value{}, err
		}
		c.Name = name.S
		env.Define(name.S, Value{Kind: KProc, Proc: c})
		return Void, nil
	default:
		return Value{}, errorf("define", "cannot define %s", Write(target))
	}
}

// makeClosure parses (params body...) where params is a symbol, a proper
// list of symbols, or a dotted list of symbols.
func makeClosure(who string, form []Value, env *Env) (*Closure, error) {
	if len(form) < 2 {
		return nil, errorf(who, "expected parameters and at least one body expression")
	}
	c := &Closure{Env: env, Body: form[1:]}
	p := form[0]
	switch p.Kind {
	case KSymbol:
		c.Rest = p.S
	case KNil:
	case KPair:
		for p.Kind == KPair {
			if p.P.Car.Kind != KSymbol {
				return nil, errorf(who, "parameter must be a symbol, got %s", Write(p.P.Car))
			}
			c.Params = append(c.Params, p.P.Car.S)
			p = p.P.Cdr
		}
		switch p.Kind {
		case KNil:
		case KSymbol:
			c.Rest = p.S
		default:
			return nil, errorf(who, "bad parameter list")
		}
	default:
		return nil, errorf(who, "bad parameter list %s", Write(p))
	}
	return c, nil
}

// evalLet handles let, named let and let*. It returns the body's last
// expression and the new environment for the caller's tail loop.
func (in *Interp) evalLet(ctx context.Context, who string, form []Value, env *Env) (Value, *Env, error) {
	if len(form) < 2 {
		return Value{}, nil, errorf(who, "expected bindings and a body")
	}
	// Named let: (let name ((v e) ...) body...)
	if who == "let" && form[0].Kind == KSymbol {
		if len(form) < 3 {
			return Value{}, nil, errorf(who, "named let needs bindings and a body")
		}
		name := form[0].S
		names, inits, err := parseBindings(who, form[1])
		if err != nil {
			return Value{}, nil, err
		}
		args := make([]Value, len(inits))
		for i, e := range inits {
			if args[i], err = in.Eval(ctx, e, env); err != nil {
				return Value{}, nil, err
			}
		}
		loopEnv := NewEnv(env)
		c := &Closure{Name: name, Params: names, Body: form[2:], Env: loopEnv}
		loopEnv.Define(name, Value{Kind: KProc, Proc: c})
		callEnv, err := bindParams(c, args)
		if err != nil {
			return Value{}, nil, err
		}
		return in.runBodyPrefix(ctx, c.Body, callEnv)
	}
	names, inits, err := parseBindings(who, form[0])
	if err != nil {
		return Value{}, nil, err
	}
	body := form[1:]
	if who == "let" {
		vals := make([]Value, len(inits))
		for i, e := range inits {
			if vals[i], err = in.Eval(ctx, e, env); err != nil {
				return Value{}, nil, err
			}
		}
		nenv := NewEnv(env)
		for i, n := range names {
			nenv.Define(n, vals[i])
		}
		return in.runBodyPrefix(ctx, body, nenv)
	}
	// let*
	nenv := env
	for i, e := range inits {
		nenv = NewEnv(nenv)
		v, err := in.Eval(ctx, e, nenv)
		if err != nil {
			return Value{}, nil, err
		}
		nenv.Define(names[i], v)
	}
	return in.runBodyPrefix(ctx, body, NewEnv(nenv))
}

// runBodyPrefix evaluates all but the last body expression and hands the
// last one back for tail evaluation.
func (in *Interp) runBodyPrefix(ctx context.Context, body []Value, env *Env) (Value, *Env, error) {
	for i := 0; i < len(body)-1; i++ {
		if _, err := in.Eval(ctx, body[i], env); err != nil {
			return Value{}, nil, err
		}
	}
	return body[len(body)-1], env, nil
}

func parseBindings(who string, b Value) (names []string, inits []Value, err error) {
	items, ok := b.Slice()
	if !ok {
		return nil, nil, errorf(who, "bindings must be a list")
	}
	for _, it := range items {
		pair, ok := it.Slice()
		if !ok || len(pair) != 2 || pair[0].Kind != KSymbol {
			return nil, nil, errorf(who, "bad binding %s; expected (name expr)", Write(it))
		}
		names = append(names, pair[0].S)
		inits = append(inits, pair[1])
	}
	return names, inits, nil
}

// evalCond returns either a final value (done) or the expression to
// evaluate in tail position.
func (in *Interp) evalCond(ctx context.Context, clauses []Value, env *Env) (next Value, done bool, v Value, err error) {
	for _, cl := range clauses {
		parts, ok := cl.Slice()
		if !ok || len(parts) == 0 {
			return Value{}, false, Value{}, errorf("cond", "bad clause %s", Write(cl))
		}
		if parts[0].Kind == KSymbol && parts[0].S == "else" {
			if len(parts) == 1 {
				return Value{}, true, Void, nil
			}
			nx, _, err := in.runBodyPrefix(ctx, parts[1:], env)
			return nx, false, Value{}, err
		}
		t, err := in.Eval(ctx, parts[0], env)
		if err != nil {
			return Value{}, false, Value{}, err
		}
		if !t.IsTrue() {
			continue
		}
		if len(parts) == 1 {
			return Value{}, true, t, nil
		}
		if parts[1].Kind == KSymbol && parts[1].S == "=>" {
			if len(parts) != 3 {
				return Value{}, false, Value{}, errorf("cond", "expected (test => proc)")
			}
			f, err := in.Eval(ctx, parts[2], env)
			if err != nil {
				return Value{}, false, Value{}, err
			}
			r, err := in.Apply(ctx, f, []Value{t})
			return Value{}, true, r, err
		}
		nx, _, err := in.runBodyPrefix(ctx, parts[1:], env)
		return nx, false, Value{}, err
	}
	return Value{}, true, Void, nil
}

// quasi implements one-level quasiquote.
func (in *Interp) quasi(ctx context.Context, x Value, env *Env) (Value, error) {
	if x.Kind != KPair {
		return x, nil
	}
	if x.P.Car.Kind == KSymbol {
		switch x.P.Car.S {
		case "unquote":
			args, ok := x.P.Cdr.Slice()
			if !ok || len(args) != 1 {
				return Value{}, errorf("unquote", "expected 1 argument")
			}
			return in.Eval(ctx, args[0], env)
		case "quasiquote":
			return Value{}, errorf("quasiquote", "nested quasiquote is not supported")
		}
	}
	var items []Value
	cur := x
	for cur.Kind == KPair {
		el := cur.P.Car
		if el.Kind == KPair && el.P.Car.Kind == KSymbol && el.P.Car.S == "unquote-splicing" {
			args, ok := el.P.Cdr.Slice()
			if !ok || len(args) != 1 {
				return Value{}, errorf("unquote-splicing", "expected 1 argument")
			}
			v, err := in.Eval(ctx, args[0], env)
			if err != nil {
				return Value{}, err
			}
			spliced, ok := v.Slice()
			if !ok {
				return Value{}, errorf("unquote-splicing", "expected a list, got %s", Write(v))
			}
			items = append(items, spliced...)
		} else {
			v, err := in.quasi(ctx, el, env)
			if err != nil {
				return Value{}, err
			}
			items = append(items, v)
		}
		cur = cur.P.Cdr
	}
	tail := cur
	if cur.Kind != KNil {
		t, err := in.quasi(ctx, cur, env)
		if err != nil {
			return Value{}, err
		}
		tail = t
	}
	out := tail
	for i := len(items) - 1; i >= 0; i-- {
		out = Cons(items[i], out)
	}
	return out, nil
}
