package scheme

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

//go:embed all:prelude
var preludeFS embed.FS

// kernelVersion changes only when eval/apply semantics change. It is part
// of the fingerprint so a semantic change is visible as a new kernel.
const kernelVersion = "b2"

// specialForms is the fixed set of reserved names: every one is parsed as
// a special form wherever it heads a list, whatever the environment binds.
// There is no API to add to it; the fingerprint covers it.
var specialForms = []string{
	"define", "lambda", "if", "let", "let*", "letrec", "begin", "set!",
	"quote", "quasiquote", "unquote", "unquote-splicing",
	"cond", "case", "else", "=>", "and", "or",
	"do", "when", "unless", "guard", "with-output-to-string",
}

// Interp is one interpreter instance: a global environment, the primitive
// table, and the guard hook.
type Interp struct {
	global  *Env
	prims   map[string]*Prim
	gate    Gate
	out     io.Writer
	isSF    map[string]bool
	baseDir string
}

// Option configures New.
type Option func(*Interp)

// WithOutput sets where display/write/newline go. Default: io.Discard —
// the host decides who sees output (ADR 10).
func WithOutput(w io.Writer) Option { return func(in *Interp) { in.out = w } }

// WithBaseDir sets the directory relative load paths resolve against
// (the repository root in a session). Default: the process working dir.
func WithBaseDir(dir string) Option { return func(in *Interp) { in.baseDir = dir } }

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

// LoadFile evaluates a file's forms in the global environment. A relative
// path resolves against the base directory (WithBaseDir).
func (in *Interp) LoadFile(ctx context.Context, path string) error {
	if !filepath.IsAbs(path) && in.baseDir != "" {
		path = filepath.Join(in.baseDir, path)
	}
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

			case "let", "let*", "letrec":
				nx, nenv, err := in.evalLet(ctx, head.S, form, env)
				if err != nil {
					return Value{}, err
				}
				x, env = nx, nenv
				continue

			case "case":
				nx, done, v, err := in.evalCase(ctx, form, env)
				if err != nil {
					return Value{}, err
				}
				if done {
					return v, nil
				}
				x = nx
				continue

			case "do":
				return in.evalDo(ctx, form, env)

			case "when", "unless":
				who := head.S
				if len(form) == 0 {
					return Value{}, errorf(who, "expected a test")
				}
				test, err := in.Eval(ctx, form[0], env)
				if err != nil {
					return Value{}, err
				}
				take := test.IsTrue()
				if who == "unless" {
					take = !take
				}
				if !take || len(form) < 2 {
					return Void, nil
				}
				for i := 0; i < len(form)-1; i++ {
					if _, err := in.Eval(ctx, form[i], env); err != nil {
						return Value{}, err
					}
				}
				x = form[len(form)-1]
				continue

			case "guard":
				return in.evalGuard(ctx, form, env)

			case "with-output-to-string":
				if len(form) == 0 {
					return Value{}, errorf("with-output-to-string", "expected a body expression")
				}
				saved := in.out
				buf := new(strings.Builder)
				in.out = buf
				var v Value
				var err error
				for i := 0; i < len(form)-1; i++ {
					if _, err = in.Eval(ctx, form[i], env); err != nil {
						in.out = saved
						return Value{}, err
					}
				}
				if err == nil {
					v, err = in.Eval(ctx, form[len(form)-1], env)
				}
				in.out = saved
				if err != nil {
					return Value{}, err
				}
				_ = v
				return Str(buf.String()), nil

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
	if who == "letrec" {
		// letrec: every binding shares one frame so a closure that closes
		// over it can see its siblings. Each initializer may reference the
		// other names (resolving through the shared frame at call time);
		// plain values must not depend on an un-yet-evaluated sibling.
		nenv := NewEnv(env)
		for _, n := range names {
			nenv.Define(n, Void) // pre-bind so names resolve during init
		}
		for i, e := range inits {
			v, err := in.Eval(ctx, e, nenv)
			if err != nil {
				return Value{}, nil, err
			}
			nenv.Define(names[i], v)
		}
		return in.runBodyPrefix(ctx, body, nenv)
	}
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

// evalDo implements the R5RS (do ((v init [step]) …) (test [e …]) cmd …).
// It is a Go loop — constant stack regardless of iteration count. The value
// of do is the value of the test clause's last expression (void if there is
// none). Steps are evaluated in parallel using pre-step values, then assigned.
func (in *Interp) evalDo(ctx context.Context, form []Value, env *Env) (Value, error) {
	if len(form) < 2 {
		return Value{}, errorf("do", "expected ((v init [step]) …) (test [e …]) [cmd …]")
	}
	// Parse bindings: (name init [step]).
	var names []string
	var inits, steps []Value
	var haveStep []bool
	binds, ok := form[0].Slice()
	if !ok {
		return Value{}, errorf("do", "bindings must be a list")
	}
	for _, b := range binds {
		parts, ok := b.Slice()
		if !ok || len(parts) < 2 || parts[0].Kind != KSymbol {
			return Value{}, errorf("do", "bad binding %s; expected (name init [step])", Write(b))
		}
		names = append(names, parts[0].S)
		inits = append(inits, parts[1])
		if len(parts) == 3 {
			steps = append(steps, parts[2])
			haveStep = append(haveStep, true)
		} else {
			steps = append(steps, Nil)
			haveStep = append(haveStep, false)
		}
	}
	// Test clause: (test [e …]).
	testClause, ok := form[1].Slice()
	if !ok || len(testClause) == 0 {
		return Value{}, errorf("do", "test clause must be (test [e …])")
	}
	test := testClause[0]
	testResult := testClause[1:]
	// Commands (body).
	commands := form[2:]

	nenv := NewEnv(env)
	for i, e := range inits {
		v, err := in.Eval(ctx, e, env)
		if err != nil {
			return Value{}, err
		}
		nenv.Define(names[i], v)
	}
	for {
		if err := ctx.Err(); err != nil {
			return Value{}, wrap("do", err)
		}
		t, err := in.Eval(ctx, test, nenv)
		if err != nil {
			return Value{}, err
		}
		if t.IsTrue() {
			if len(testResult) == 0 {
				return Void, nil
			}
			for i := 0; i < len(testResult)-1; i++ {
				if _, err := in.Eval(ctx, testResult[i], nenv); err != nil {
					return Value{}, err
				}
			}
			return in.Eval(ctx, testResult[len(testResult)-1], nenv)
		}
		// Steps: evaluate in parallel (all see pre-step values), then assign.
		newVals := make([]Value, len(inits))
		for i, s := range steps {
			if !haveStep[i] {
				cur, _ := nenv.Lookup(names[i])
				newVals[i] = cur
				continue
			}
			v, err := in.Eval(ctx, s, nenv)
			if err != nil {
				return Value{}, err
			}
			newVals[i] = v
		}
		for i, nv := range newVals {
			nenv.Define(names[i], nv)
		}
		for _, c := range commands {
			if _, err := in.Eval(ctx, c, nenv); err != nil {
				return Value{}, err
			}
		}
	}
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

// evalGuard implements R7RS-shaped (guard (var clause ...) body ...): the
// body is evaluated; if it raises a Scheme error, var is bound to the
// condition -- an alist ((message . "text") (irritants ...)) -- and the
// clauses are tried like cond (test body ..., test => proc, else). The
// first true clause's value is the value of guard; no match re-raises.
//
// Two things are never catchable, whatever the clauses say: a guard
// denial and a cancelled context. A tool must not be able to hide that it
// was refused, and Abort must always unwind.
func (in *Interp) evalGuard(ctx context.Context, form []Value, env *Env) (Value, error) {
	if len(form) < 2 {
		return Value{}, errorf("guard", "expected (guard (var clause ...) body ...)")
	}
	spec, ok := form[0].Slice()
	if !ok || len(spec) == 0 || spec[0].Kind != KSymbol {
		return Value{}, errorf("guard", "expected (var clause ...) as the first argument")
	}
	varName := spec[0].S
	clauses := spec[1:]
	body := form[1:]

	last, err := in.evalBody(ctx, body, env)
	if err == nil {
		return last, nil
	}
	if !catchable(ctx, err) {
		return Value{}, err
	}
	var se *Error
	if !errors.As(err, &se) {
		return Value{}, err
	}
	cond := List(
		Cons(Sym("message"), Str(se.Message)),
		Cons(Sym("irritants"), List(se.Irritants...)),
	)
	henv := NewEnv(env)
	henv.Define(varName, cond)
	nx, done, v, matched, cerr := in.condClauses(ctx, clauses, henv)
	if cerr != nil {
		return Value{}, cerr
	}
	if !matched {
		return Value{}, err // no clause matched: re-raise the original
	}
	if done {
		return v, nil
	}
	return in.Eval(ctx, nx, henv)
}

// catchable reports whether a Scheme program may handle err: denials and
// cancellation are not.
func catchable(ctx context.Context, err error) bool {
	var d *Denied
	if errors.As(err, &d) {
		return false
	}
	if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	return true
}

// evalCase implements (case key [(datum ...) body ...] ... [(else body ...)]).
// Datums are compared with equal? (symbols by exact name, like the rest of
// the language).
func (in *Interp) evalCase(ctx context.Context, form []Value, env *Env) (next Value, done bool, v Value, err error) {
	if len(form) < 2 {
		return Value{}, false, Value{}, errorf("case", "expected (key (datum …) (body …))")
	}
	key, err := in.Eval(ctx, form[0], env)
	if err != nil {
		return Value{}, false, Value{}, err
	}
	for _, clause := range form[1:] {
		parts, isList := clause.Slice()
		if !isList || len(parts) < 2 {
			return Value{}, false, Value{}, errorf("case", "bad clause %s", Write(clause))
		}
		if parts[0].Kind == KSymbol && parts[0].S == "else" {
			nx, _, e := in.runBodyPrefix(ctx, parts[1:], env)
			return nx, false, Value{}, e
		}
		datums, isList := parts[0].Slice()
		if !isList {
			return Value{}, false, Value{}, errorf("case", "datums must be a list")
		}
		matched := false
		for _, d := range datums {
			if Equal(d, key) {
				matched = true
				break
			}
		}
		if matched {
			nx, _, e := in.runBodyPrefix(ctx, parts[1:], env)
			return nx, false, Value{}, e
		}
	}
	// No clause matched and no else.
	return Value{}, true, Void, nil
}

// evalCond returns either a final value (done) or the expression to
// evaluate in tail position.
func (in *Interp) evalCond(ctx context.Context, clauses []Value, env *Env) (next Value, done bool, v Value, err error) {
	next, done, v, _, err = in.condClauses(ctx, clauses, env)
	return
}

// condClauses is cond's clause walk, shared with guard; matched reports
// whether any clause (including else) was selected.
func (in *Interp) condClauses(ctx context.Context, clauses []Value, env *Env) (next Value, done bool, v Value, matched bool, err error) {
	for _, cl := range clauses {
		parts, ok := cl.Slice()
		if !ok || len(parts) == 0 {
			return Value{}, false, Value{}, false, errorf("cond", "bad clause %s", Write(cl))
		}
		if parts[0].Kind == KSymbol && parts[0].S == "else" {
			if len(parts) == 1 {
				return Value{}, true, Void, true, nil
			}
			nx, _, err := in.runBodyPrefix(ctx, parts[1:], env)
			return nx, false, Value{}, true, err
		}
		t, err := in.Eval(ctx, parts[0], env)
		if err != nil {
			return Value{}, false, Value{}, false, err
		}
		if !t.IsTrue() {
			continue
		}
		if len(parts) == 1 {
			return Value{}, true, t, true, nil
		}
		if parts[1].Kind == KSymbol && parts[1].S == "=>" {
			if len(parts) != 3 {
				return Value{}, false, Value{}, false, errorf("cond", "expected (test => proc)")
			}
			f, err := in.Eval(ctx, parts[2], env)
			if err != nil {
				return Value{}, false, Value{}, false, err
			}
			r, err := in.Apply(ctx, f, []Value{t})
			return Value{}, true, r, true, err
		}
		nx, _, err := in.runBodyPrefix(ctx, parts[1:], env)
		return nx, false, Value{}, true, err
	}
	return Value{}, true, Void, false, nil
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
