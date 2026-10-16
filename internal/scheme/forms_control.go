package scheme

import (
	"context"
	"errors"
)

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
		// Commands see this iteration's values, before the steps (R5RS).
		for _, c := range commands {
			if _, err := in.Eval(ctx, c, nenv); err != nil {
				return Value{}, err
			}
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
	}
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
	if !Catchable(ctx, err) {
		return Value{}, err
	}
	var se *Error
	if !errors.As(err, &se) {
		return Value{}, err
	}
	cond := List(
		Cons(Sym("message"), Value{Kind: KString, S: se.Message, Taint: se.Taint}),
		Cons(Sym("irritants"), List(se.Irritants...)),
	)
	henv := NewEnv(env)
	henv.Define(varName, cond)
	result, cerr := in.condClauses(ctx, clauses, henv)
	if cerr != nil {
		return Value{}, cerr
	}
	if !result.matched {
		return Value{}, err // no clause matched: re-raise the original
	}
	if result.done {
		return result.value, nil
	}
	return in.Eval(ctx, result.next, henv)
}

// Catchable reports whether a Scheme program may handle err. A denial, a
// Halt and the turn's own cancellation are not: a program cannot hide that
// it was refused, keep going after the harness stopped it, or outlive an
// abort. A primitive's own deadline, under a turn that goes on (a judge's
// ten seconds, an MCP call's two minutes), is that call failing, which a
// strategy may handle: the plan gate plans anyway when the judge does not
// answer. A host primitive that goes on past one file's error asks it too.
func Catchable(ctx context.Context, err error) bool {
	var d *Denied
	if errors.As(err, &d) {
		return false
	}
	var h *Halt
	if errors.As(err, &h) {
		return false
	}
	return ctx.Err() == nil
}

// branchResult describes a selected clause without positional return flags.
// A completed branch has a value; otherwise next is evaluated in tail position.
// matched distinguishes a cond fallthrough from a selected void-valued clause.
type branchResult struct {
	next    Value
	value   Value
	done    bool
	matched bool
}

// evalCase implements (case key [(datum ...) body ...] ... [(else body ...)]).
// Datums are compared with equal? (symbols by exact name, like the rest of
// the language).
func (in *Interp) evalCase(ctx context.Context, form []Value, env *Env) (branchResult, error) {
	if len(form) < 2 {
		return branchResult{}, errorf("case", "expected (key (datum …) (body …))")
	}
	key, err := in.Eval(ctx, form[0], env)
	if err != nil {
		return branchResult{}, err
	}
	for _, clause := range form[1:] {
		parts, isList := clause.Slice()
		if !isList || len(parts) < 2 {
			return branchResult{}, errorf("case", "bad clause %s", Write(clause))
		}
		if parts[0].Kind == KSymbol && parts[0].S == "else" {
			nx, _, e := in.runBodyPrefix(ctx, parts[1:], env)
			return branchResult{next: nx, matched: true}, e
		}
		datums, isList := parts[0].Slice()
		if !isList {
			return branchResult{}, errorf("case", "datums must be a list")
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
			return branchResult{next: nx, matched: true}, e
		}
	}
	// No clause matched and no else.
	return branchResult{done: true, value: Void}, nil
}

// condClauses is cond's clause walk, shared with guard; matched reports
// whether any clause (including else) was selected.
func (in *Interp) condClauses(ctx context.Context, clauses []Value, env *Env) (branchResult, error) {
	for _, cl := range clauses {
		parts, ok := cl.Slice()
		if !ok || len(parts) == 0 {
			return branchResult{}, errorf("cond", "bad clause %s", Write(cl))
		}
		if parts[0].Kind == KSymbol && parts[0].S == "else" {
			if len(parts) == 1 {
				return branchResult{done: true, value: Void, matched: true}, nil
			}
			nx, _, err := in.runBodyPrefix(ctx, parts[1:], env)
			return branchResult{next: nx, matched: true}, err
		}
		t, err := in.Eval(ctx, parts[0], env)
		if err != nil {
			return branchResult{}, err
		}
		if !t.IsTrue() {
			continue
		}
		if len(parts) == 1 {
			return branchResult{done: true, value: t, matched: true}, nil
		}
		if parts[1].Kind == KSymbol && parts[1].S == "=>" {
			if len(parts) != 3 {
				return branchResult{}, errorf("cond", "expected (test => proc)")
			}
			f, err := in.Eval(ctx, parts[2], env)
			if err != nil {
				return branchResult{}, err
			}
			r, err := in.Apply(ctx, f, []Value{t})
			return branchResult{done: true, value: r, matched: true}, err
		}
		nx, _, err := in.runBodyPrefix(ctx, parts[1:], env)
		return branchResult{next: nx, matched: true}, err
	}
	return branchResult{done: true, value: Void}, nil
}
