package scheme

import (
	"context"
	"fmt"
	"sort"
)

// Param documents one positional parameter of a primitive. Type is one of
// string integer number boolean list alist any (docs/scheme.md §7). Role is
// what the argument *is* to the guard (path, source, dest, payload,
// credential) and is empty for plain data. A source is a file the
// primitive only reads; a path is what it acts on, and writes when it is
// irreversible.
type Param struct {
	Name string
	Type string
	Doc  string
	Role string
	// Optional parameters come last (checked at Register). A call may stop
	// before any of them: the handler sees fewer arguments, never a
	// placeholder, and branches on len(c.Args). Go-only for now.
	Optional bool
	// Items describes the fields of each element of a list parameter whose
	// elements are objects (alists with string keys, as json-parse makes
	// them): the tool schema shows them to the model, and the guard reads a
	// field's Role as it reads a parameter's (EachRole). Go-only.
	Items []Param
}

// EachRole calls fn with every argument that has a role and, inside a list
// parameter with Items, every element's field that has one: fs-edit's
// edits carry each replacement as a payload, as its new does. A field
// carries the taint of the list and the element around it too, since a
// list parsed from tool output may carry that taint only on the outside.
func (p *Prim) EachRole(args []Value, fn func(role string, v Value)) {
	for i, pa := range p.Meta.Params {
		if i >= len(args) {
			return
		}
		if pa.Role != "" {
			fn(pa.Role, args[i])
		}
		if len(pa.Items) > 0 {
			eachItemRole(pa.Items, args[i], fn)
		}
	}
}

// eachItemRole walks a list of alists for the fields that have a role.
// Pairs are immutable, so the walk ends.
func eachItemRole(fields []Param, list Value, fn func(role string, v Value)) {
	outer := list.Taint
	for l := list; l.Kind == KPair; l = l.P.Cdr {
		outer = Union(outer, l.Taint)
		inner := Union(outer, l.P.Car.Taint)
		for f := l.P.Car; f.Kind == KPair; f = f.P.Cdr {
			inner = Union(inner, f.Taint)
			kv := f.P.Car
			if kv.Kind != KPair || kv.P.Car.Kind != KString {
				continue
			}
			for _, fp := range fields {
				if fp.Role != "" && fp.Name == kv.P.Car.S {
					v := kv.P.Cdr
					v.Taint = Union(v.Taint, inner, kv.Taint)
					fn(fp.Role, v)
				}
			}
		}
	}
}

// required is how many of p's parameters every call must give.
func required(p *Prim) int {
	n := 0
	for _, pa := range p.Meta.Params {
		if !pa.Optional {
			n++
		}
	}
	return n
}

// Meta is a primitive's declaration (ADR 4). The guard reads only this.
type Meta struct {
	Doc          string
	Params       []Param
	Variadic     bool // extra arguments allowed after Params
	Egress       bool // may send bytes off-machine
	Irreversible bool // cannot be undone by the harness
	Host         bool // touches the OS: filesystem, processes, network
	Hidden       bool // never offered to the model as a tool (e.g. load)
	Audited      bool // always record the guard decision, even an allow

	// TaintSource tags this primitive's result: what the value's provenance
	// is (L2, Phase 6). Empty means the result carries only the taint its
	// arguments carried.
	TaintSource string
	// TaintOpaque means the result carries no taint from the arguments: it
	// is a measurement of them, not made of them (length, a predicate, a
	// comparison). Opting out is explicit and rare; propagating is the
	// default.
	TaintOpaque bool
	// TaintStrip marks a primitive that removes taint (wipe, box). The
	// guard refuses it from a model-originated call.
	TaintStrip bool
	Origin     string // "builtin" or "scheme"
	// EgressHost is the fixed destination host for egress prims that do not
	// carry a "dest" role argument (e.g. Jev, ADR 20). The E3 binding checker
	// uses this in place of extracting the host from a URL argument.
	EgressHost string
	// Fixed scope for host integrations without method/position parameters.
	// Go-only: Scheme extensions cannot declare a different host operation.
	EgressMethod   string
	EgressPosition string
	// EgressClass is the fixed credential class a host integration injects
	// itself (an MCP server's key, ADR 22 §4), for a primitive with no
	// credential argument: the call is judged as carrying that class. Go-only.
	EgressClass string
	// EgressApproved means the person named EgressHost outside the binding
	// table (an MCP server approved in mcp.toml, ADR 23): L2 counts it as a
	// named destination, as it would a host a binding names, for this
	// primitive only. Go-only.
	EgressApproved bool
	// EgressContained marks a contained MCP server's tool (ADR 23 W3b): the
	// call goes to a container (EgressHost names it), which may send it on
	// to the hosts in EgressOnward and nowhere else. A literal key, or a
	// value derived from a credential, in its arguments is then bound only
	// if its class is bound for every one of them; with none, for none.
	// Go-only.
	EgressContained bool
	EgressOnward    []string
	// ConfirmReason, when set, is why an irreversible call asks, in the
	// integration's words (an MCP tool nobody declared). Go-only.
	ConfirmReason string

	// Preview, for irreversible host primitives, renders what the call
	// would do (a diff) for the confirm prompt. Go-only; not settable from
	// register-tool.
	Preview func(args []Value) string
}

// Call is what a primitive handler receives. Origin is who initiated the
// evaluation (see OriginFrom).
type Call struct {
	Ctx    context.Context
	In     *Interp
	Name   string
	Args   []Value
	Origin Origin
}

// Prim is an entry in the primitive table: the only Scheme→Go surface.
type Prim struct {
	Name    string
	Meta    Meta
	Handler func(c *Call) (Value, error)
	// SkipsAsVoid makes an optional parameter the model leaves out before
	// one it gives arrive as void rather than its type's empty value, for a
	// handler that must tell "not given" from #f, 0 or "" (an MCP tool,
	// whose server applies its own default to what is not sent).
	SkipsAsVoid bool
}

// Gate is the guard's hook at the single Scheme→Go exit. Before returns nil
// to allow, or an error (conventionally a *Denied) to refuse. It may block
// (to ask the user) and must honour ctx.
//
// RegisterChecker (register-check, ADR 20) lets user-originated Scheme code
// add a tightening-only checker to the guard pipeline. The predicate is a
// closure (prim-name args) → boolean; when it returns #t the checker
// returns the fixed verdict ("deny" or "confirm"); otherwise it returns
// Allow (no opinion). The strictest-wins rule guarantees a user checker
// can only tighten, never relax.
type Gate interface {
	Before(ctx context.Context, p *Prim, args []Value) error
	RegisterChecker(name, verdict string, predicate Value) error
}

// Register adds a primitive. Names are unique for the life of the
// interpreter: builtins cannot be shadowed and tools cannot be re-registered
// (ADR 9 needs a stable tool list).
func (in *Interp) Register(p *Prim) error {
	if p.Name == "" || p.Handler == nil {
		return fmt.Errorf("scheme: primitive needs a name and a handler")
	}
	if in.frozen {
		return fmt.Errorf("scheme: primitive %q cannot be registered now: the tool list is fixed for the session (ADR 9)", p.Name)
	}
	if p.Meta.Doc == "" {
		return fmt.Errorf("scheme: primitive %q has no doc", p.Name)
	}
	if p.Meta.Origin == "" {
		p.Meta.Origin = "builtin"
	}
	if _, dup := in.prims[p.Name]; dup {
		return fmt.Errorf("scheme: primitive %q already registered", p.Name)
	}
	optional := false
	for _, pa := range p.Meta.Params {
		if pa.Optional {
			optional = true
		} else if optional {
			return fmt.Errorf("scheme: primitive %q: required parameter %q follows an optional one", p.Name, pa.Name)
		}
	}
	if optional && p.Meta.Variadic {
		return fmt.Errorf("scheme: primitive %q: a variadic primitive cannot have optional parameters", p.Name)
	}
	in.prims[p.Name] = p
	in.global.Define(p.Name, Value{Kind: KPrim, Prim: p})
	return nil
}

// Lookup returns a primitive by name.
func (in *Interp) Lookup(name string) (*Prim, bool) {
	p, ok := in.prims[name]
	return p, ok
}

// Prims returns every primitive, sorted by name.
func (in *Interp) Prims() []*Prim {
	out := make([]*Prim, 0, len(in.prims))
	for _, p := range in.prims {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// SetGate installs the guard. nil allows everything: an embedder with no guard, such as a test.
func (in *Interp) SetGate(g Gate) { in.gate = g }

// SetExpand installs a rewrite of path and command arguments (parameters
// with Role "path", "source" or "argv", and the strings of an argv list),
// applied before the guard, so the guard and the handler judge and use the
// same value. The session uses it to give $TMPDIR one meaning in every tool.
func (in *Interp) SetExpand(f func(string) string) { in.expand = f }

// expandArgs applies the rewrite to p's path and argv arguments.
func (in *Interp) expandArgs(p *Prim, args []Value) []Value {
	if in.expand == nil {
		return args
	}
	out := args
	copied := false
	set := func(i int, v Value) {
		if !copied {
			out = append([]Value(nil), args...)
			copied = true
		}
		out[i] = v
	}
	for i, pa := range p.Meta.Params {
		if i >= len(args) || (pa.Role != "path" && pa.Role != "source" && pa.Role != "argv") {
			continue
		}
		switch a := args[i]; a.Kind {
		case KString:
			if s := in.expand(a.S); s != a.S {
				a.S = s
				set(i, a)
			}
		default:
			items, ok := a.Slice()
			if pa.Role != "argv" || !ok {
				continue
			}
			changed := false
			for j, it := range items {
				if it.Kind == KString {
					if s := in.expand(it.S); s != it.S {
						it.S = s
						items[j] = it
						changed = true
					}
				}
			}
			if changed {
				l := List(items...)
				l.Taint = a.Taint
				set(i, l)
			}
		}
	}
	return out
}

// applyPrim is the single Scheme→Go exit. Every primitive application —
// from the model, the prelude, a .scm file or the REPL — passes through
// here: arity check, guard, handler. Nothing else calls p.Handler.
func (in *Interp) applyPrim(ctx context.Context, p *Prim, args []Value) (Value, error) {
	if err := ctx.Err(); err != nil {
		return Value{}, wrap(p.Name, err)
	}
	if err := checkArity(p, args); err != nil {
		return Value{}, err
	}
	args = in.expandArgs(p, args)
	if in.gate != nil {
		if err := in.gate.Before(ctx, p, args); err != nil {
			return Value{}, wrap(p.Name, err)
		}
	}
	v, err := p.Handler(&Call{Ctx: ctx, In: in, Name: p.Name, Args: args, Origin: OriginFrom(ctx)})
	if err != nil {
		// The message may quote the arguments, so a guard clause that
		// reads it gets their taint with it.
		se := wrap(p.Name, err)
		se.Taint = Union(se.Taint, argsTaint(p, args))
		return Value{}, se
	}
	return taintResult(p, args, v), nil
}

// taintResult is where a primitive's result takes provenance (L2, Phase 6):
// the union of its arguments' taint unless the primitive declares itself
// opaque, plus the primitive's own source tag if it has one. A list keeps
// each element's taint on the element, so the guard can tell a field's from
// its neighbours'; a string made from a list or a vector (string-join,
// json-emit) takes everything the list holds, as do the output
// with-output-to-string captures and the text of an error.
func taintResult(p *Prim, args []Value, v Value) Value {
	if p.Meta.TaintStrip {
		v.Taint = Taint{}
		return v
	}
	var t Taint
	if v.Kind == KString {
		t = argsTaint(p, args)
	} else if !p.Meta.TaintOpaque {
		for _, a := range args {
			t = Union(t, a.Taint)
		}
	}
	if p.Meta.TaintSource != "" {
		t = Union(t, TaintOf(p.Meta.TaintSource))
	}
	v.Taint = Union(v.Taint, t)
	return v
}

// argsTaint is the union of the arguments' taint and of everything they
// hold; none for an opaque primitive.
func argsTaint(p *Prim, args []Value) Taint {
	var t Taint
	if p.Meta.TaintOpaque {
		return t
	}
	for _, a := range args {
		t = Union(t, DeepTaint(a))
	}
	return t
}

// DeepTaint is v's taint and that of everything it holds, through pairs and
// vectors, to MaxDepth. A vector may hold itself, so each is walked once.
func DeepTaint(v Value) Taint {
	return deepTaint(v, 0, map[*Vector]bool{})
}

func deepTaint(v Value, depth int, seen map[*Vector]bool) Taint {
	t := v.Taint
	for ; depth <= MaxDepth; depth++ {
		switch v.Kind {
		case KPair:
			t = Union(t, deepTaint(v.P.Car, depth+1, seen), v.P.Cdr.Taint)
			v = v.P.Cdr
			continue
		case KVec:
			if seen[v.V] {
				return t
			}
			seen[v.V] = true
			for _, item := range v.V.Items {
				t = Union(t, deepTaint(item, depth+1, seen))
			}
		}
		return t
	}
	return t
}

func checkArity(p *Prim, args []Value) error {
	want := len(p.Meta.Params)
	got := len(args)
	if min := required(p); min < want {
		if got < min || got > want {
			return errorf(p.Name, "expected %d to %d argument(s), got %d", min, want, got)
		}
		return nil
	}
	switch {
	case got == want:
		return nil
	case got > want && p.Meta.Variadic:
		return nil
	case p.Meta.Variadic:
		return errorf(p.Name, "expected at least %d argument(s), got %d", want, got)
	default:
		return errorf(p.Name, "expected %d argument(s), got %d", want, got)
	}
}

// Argument accessors for handlers. Each returns a typed runtime error.

func (c *Call) arg(i int) Value {
	if i < len(c.Args) {
		return c.Args[i]
	}
	return Void
}

func (c *Call) typeErr(i int, want string) error {
	return errorf(c.Name, "argument %d: expected %s, got %s %s", i+1, want, c.arg(i).Kind, Write(c.arg(i)))
}

// String returns argument i as a Go string.
func (c *Call) String(i int) (string, error) {
	if v := c.arg(i); v.Kind == KString {
		return v.S, nil
	}
	return "", c.typeErr(i, "string")
}

// Int returns argument i as int64.
func (c *Call) Int(i int) (int64, error) {
	if v := c.arg(i); v.Kind == KInt {
		return v.I, nil
	}
	return 0, c.typeErr(i, "integer")
}

// Number returns argument i, checked to be a number.
func (c *Call) Number(i int) (Value, error) {
	if v := c.arg(i); v.IsNumber() {
		return v, nil
	}
	return Value{}, c.typeErr(i, "number")
}

// List returns argument i as a slice, checked to be a proper list.
func (c *Call) List(i int) ([]Value, error) {
	if s, ok := c.arg(i).Slice(); ok {
		return s, nil
	}
	return nil, c.typeErr(i, "list")
}

// Pair returns argument i, checked to be a pair.
func (c *Call) Pair(i int) (*Pair, error) {
	if v := c.arg(i); v.Kind == KPair {
		return v.P, nil
	}
	return nil, c.typeErr(i, "pair")
}

// Proc returns argument i, checked to be a procedure.
func (c *Call) Proc(i int) (Value, error) {
	if v := c.arg(i); v.IsProcedure() {
		return v, nil
	}
	return Value{}, c.typeErr(i, "procedure")
}

// Symbol returns argument i as a symbol name.
func (c *Call) Symbol(i int) (string, error) {
	if v := c.arg(i); v.Kind == KSymbol {
		return v.S, nil
	}
	return "", c.typeErr(i, "symbol")
}

// Tool reports whether the model may call p directly: host primitives and
// Scheme-registered tools, except those explicitly hidden.
func (p *Prim) Tool() bool {
	if p.Meta.Hidden {
		return false
	}
	return p.Meta.Host || p.Meta.Origin == "scheme"
}

// Tools returns the primitives exposed to the model, sorted by name (a
// stable order keeps the request prefix stable, ADR 9).
func (in *Interp) Tools() []*Prim {
	var out []*Prim
	for _, p := range in.Prims() {
		if p.Tool() {
			out = append(out, p)
		}
	}
	return out
}
