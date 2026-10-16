package scheme

import (
	"context"
	"embed"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
)

//go:embed all:prelude
var preludeFS embed.FS

// specialForms is the fixed set of reserved names: every one is parsed as
// a special form wherever it heads a list, whatever the environment binds.
// There is no API to add to it.
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
	expand  func(string) string // rewrites path and argv arguments before the guard; nil for none
	out     io.Writer
	isSF    map[string]bool
	baseDir string

	// reloading is set while a session re-runs its init file: what would
	// change the tool list or the guard it started with is noted in
	// reloadNotes instead of applied (ADR 9).
	reloading   bool
	reloadNotes []string
	// frozen is set by Freeze, once the tools a session offers have been
	// registered: after it no primitive can be added (ADR 9).
	frozen bool

	// depth counts the Evals in progress, nested ones included. Tail calls
	// loop inside one Eval and do not add to it; MaxDepth bounds it, so a
	// runaway recursion is an error the program can catch, not a Go stack
	// overflow, which ends the process and every session in it.
	depth atomic.Int64
	// loading holds the files LoadFile is evaluating, so a file that loads
	// itself, directly or through another, is refused.
	loading map[string]bool
	// printed is the taint of what display and write have printed since
	// with-output-to-string began capturing; the string it returns has it.
	printed Taint
}

// MaxDepth is how deeply evaluation, and the reader, the printer, equal? and
// json-emit, may nest. A nested Eval takes one to five kilobytes of Go stack
// (more when it recurses through a primitive such as map), so the limit costs
// at most some 50 MB, far inside Go's gigabyte even with sub-agents nested
// above it. Tail calls do not nest, and the default loop's own walks are tail
// calls, so only a recursion over thousands of items that is not one meets it.
const MaxDepth = 10_000

// BeginReload starts re-running an init file over this interpreter:
// register-tool replaces the body of a tool whose name, description and
// parameters are unchanged, and notes any other registration instead of
// applying it; register-check is noted, not applied.
func (in *Interp) BeginReload() { in.reloading, in.reloadNotes = true, nil }

// EndReload ends it and returns what could not change in this session.
func (in *Interp) EndReload() []string {
	notes := in.reloadNotes
	in.reloading, in.reloadNotes = false, nil
	return notes
}

func (in *Interp) noteReload(format string, args ...any) {
	in.reloadNotes = append(in.reloadNotes, fmt.Sprintf(format, args...))
}

// Freeze ends registration: Register, and so register-tool, fail from here
// on. The loop calls it when it fixes the tool list, so a tool body that
// registers a helper later cannot add a model-callable primitive that was
// never offered.
func (in *Interp) Freeze() { in.frozen = true }

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
	if abs, err := filepath.Abs(path); err == nil {
		if in.loading[abs] {
			return fmt.Errorf("%s: loaded again while it is being loaded", path)
		}
		if in.loading == nil {
			in.loading = map[string]bool{}
		}
		in.loading[abs] = true
		defer delete(in.loading, abs)
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
	if in.depth.Add(1) > MaxDepth {
		in.depth.Add(-1)
		return Value{}, errorf("eval", "recursion too deep: more than %d nested evaluations", MaxDepth)
	}
	defer in.depth.Add(-1)
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
				result, err := in.evalCase(ctx, form, env)
				if err != nil {
					return Value{}, err
				}
				if result.done {
					return result.value, nil
				}
				x = result.next
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
				saved, savedTaint := in.out, in.printed
				buf := new(strings.Builder)
				in.out, in.printed = buf, Taint{}
				defer func() { in.printed = savedTaint }()
				var err error
				for _, f := range form {
					if _, err = in.Eval(ctx, f, env); err != nil {
						break
					}
				}
				in.out = saved
				if err != nil {
					return Value{}, err
				}
				return Value{Kind: KString, S: buf.String(), Taint: in.printed}, nil

			case "cond":
				result, err := in.condClauses(ctx, form, env)
				if err != nil {
					return Value{}, err
				}
				if result.done {
					return result.value, nil
				}
				x = result.next
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
