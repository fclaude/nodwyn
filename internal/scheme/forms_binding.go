package scheme

import (
	"context"
)

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

// evalLet handles let, named let, let* and letrec. It returns the body's last
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
