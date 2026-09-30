package scheme

import (
	"context"
)

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
	for first := true; cur.Kind == KPair; first = false {
		// (a . ,b) reads as (a unquote b): an unquote in the tail is the
		// tail's value, not two more elements.
		if !first && cur.P.Car.Kind == KSymbol && cur.P.Car.S == "unquote" {
			break
		}
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
