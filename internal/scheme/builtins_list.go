package scheme

import (
	"sort"
)

// maxIota bounds (iota n): the list is built in one Go loop that the context
// is not checked inside, so a huge n would run out of memory, not abort.
const maxIota = 1_000_000

func (in *Interp) registerLists() {
	any1 := []Param{{Name: "x", Type: "any"}}
	lst := []Param{{Name: "l", Type: "list"}}
	pr := []Param{{Name: "p", Type: "list"}}

	in.mustRegister("cons", "Build a pair.", []Param{{Name: "a", Type: "any"}, {Name: "d", Type: "any"}}, false, func(c *Call) (Value, error) {
		return Cons(c.Args[0], c.Args[1]), nil
	})
	in.mustRegister("car", "First element of a pair.", pr, false, func(c *Call) (Value, error) {
		p, err := c.Pair(0)
		if err != nil {
			return Value{}, err
		}
		return p.Car, nil
	})
	in.mustRegister("cdr", "Rest of a pair.", pr, false, func(c *Call) (Value, error) {
		p, err := c.Pair(0)
		if err != nil {
			return Value{}, err
		}
		return p.Cdr, nil
	})
	// c[ad]+r compositions: the path is applied right to left.
	for _, name := range []string{"caar", "cadr", "cdar", "cddr", "caddr", "cdddr"} {
		path := name[1 : len(name)-1]
		in.mustRegister(name, "Composition of car/cdr: "+name+".", pr, false, func(c *Call) (Value, error) {
			v := c.Args[0]
			for i := len(path) - 1; i >= 0; i-- {
				if v.Kind != KPair {
					return Value{}, errorf(c.Name, "expected a pair, got %s", Write(c.Args[0]))
				}
				if path[i] == 'a' {
					v = v.P.Car
				} else {
					v = v.P.Cdr
				}
			}
			return v, nil
		})
	}
	in.mustRegister("list", "Build a proper list of the arguments.", nil, true, func(c *Call) (Value, error) {
		return List(c.Args...), nil
	})
	in.mustRegisterOpaque("list?", "True for proper lists.", any1, false, func(c *Call) (Value, error) {
		return Bool(c.Args[0].IsList()), nil
	})
	in.mustRegisterOpaque("pair?", "True for pairs.", any1, false, func(c *Call) (Value, error) {
		return Bool(c.Args[0].Kind == KPair), nil
	})
	in.mustRegisterOpaque("null?", "True for the empty list.", any1, false, func(c *Call) (Value, error) {
		return Bool(c.Args[0].Kind == KNil), nil
	})
	in.mustRegisterOpaque("length", "Number of elements in a proper list.", lst, false, func(c *Call) (Value, error) {
		s, err := c.List(0)
		if err != nil {
			return Value{}, err
		}
		return Int(int64(len(s))), nil
	})
	in.mustRegister("append", "Concatenate lists; the last argument may be any value.", nil, true, func(c *Call) (Value, error) {
		if len(c.Args) == 0 {
			return Nil, nil
		}
		out := c.Args[len(c.Args)-1]
		for i := len(c.Args) - 2; i >= 0; i-- {
			s, err := c.List(i)
			if err != nil {
				return Value{}, err
			}
			for j := len(s) - 1; j >= 0; j-- {
				out = Cons(s[j], out)
			}
		}
		return out, nil
	})
	in.mustRegister("reverse", "Reverse a list.", lst, false, func(c *Call) (Value, error) {
		s, err := c.List(0)
		if err != nil {
			return Value{}, err
		}
		out := Nil
		for _, v := range s {
			out = Cons(v, out)
		}
		return out, nil
	})
	in.mustRegister("list-ref", "Element at 0-based index i.", []Param{{Name: "l", Type: "list"}, {Name: "i", Type: "integer"}}, false, func(c *Call) (Value, error) {
		s, err := c.List(0)
		if err != nil {
			return Value{}, err
		}
		i, err := c.Int(1)
		if err != nil {
			return Value{}, err
		}
		if i < 0 || i >= int64(len(s)) {
			return Value{}, errorf(c.Name, "index %d out of range for list of length %d", i, len(s))
		}
		return s[i], nil
	})
	in.mustRegister("list-tail", "The list after dropping i elements.", []Param{{Name: "l", Type: "list"}, {Name: "i", Type: "integer"}}, false, func(c *Call) (Value, error) {
		v := c.Args[0]
		i, err := c.Int(1)
		if err != nil {
			return Value{}, err
		}
		for k := int64(0); k < i; k++ {
			if v.Kind != KPair {
				return Value{}, errorf(c.Name, "index %d out of range", i)
			}
			v = v.P.Cdr
		}
		return v, nil
	})
	in.mustRegister("iota", "The list (0 1 ... n-1); n is at most 1000000.", []Param{{Name: "n", Type: "integer"}}, false, func(c *Call) (Value, error) {
		n, err := c.Int(0)
		if err != nil {
			return Value{}, err
		}
		if n < 0 {
			return Value{}, errorf(c.Name, "n must be non-negative")
		}
		if n > maxIota {
			return Value{}, errorf(c.Name, "n is %d; a list of more than %d elements is refused", n, maxIota)
		}
		out := Nil
		for i := n - 1; i >= 0; i-- {
			out = Cons(Int(i), out)
		}
		return out, nil
	})
	mem := func(name, doc string, eq func(a, b Value) bool) {
		in.mustRegister(name, doc, []Param{{Name: "x", Type: "any"}, {Name: "l", Type: "list"}}, false, func(c *Call) (Value, error) {
			v := c.Args[1]
			for v.Kind == KPair {
				if eq(c.Args[0], v.P.Car) {
					return v, nil
				}
				v = v.P.Cdr
			}
			return False, nil
		})
	}
	mem("memq", "Tail of l starting at the first element eq? to x, else #f.", Eqv)
	mem("member", "Tail of l starting at the first element equal? to x, else #f.", Equal)
	ass := func(name, doc string, eq func(a, b Value) bool) {
		in.mustRegister(name, doc, []Param{{Name: "k", Type: "any"}, {Name: "al", Type: "alist"}}, false, func(c *Call) (Value, error) {
			v := c.Args[1]
			for v.Kind == KPair {
				if e := v.P.Car; e.Kind == KPair && eq(c.Args[0], e.P.Car) {
					return e, nil
				}
				v = v.P.Cdr
			}
			return False, nil
		})
	}
	ass("assq", "First pair in al whose car is eq? to k, else #f.", Eqv)
	ass("assoc", "First pair in al whose car is equal? to k, else #f.", Equal)

	in.mustRegister("map", "Apply f elementwise across one or more lists; stops at the shortest.", []Param{{Name: "f", Type: "any"}, {Name: "l", Type: "list"}}, true, func(c *Call) (Value, error) {
		out, err := applyAcross(c, true)
		if err != nil {
			return Value{}, err
		}
		return List(out...), nil
	})
	in.mustRegister("for-each", "Apply f elementwise for effect; yields void.", []Param{{Name: "f", Type: "any"}, {Name: "l", Type: "list"}}, true, func(c *Call) (Value, error) {
		if _, err := applyAcross(c, false); err != nil {
			return Value{}, err
		}
		return Void, nil
	})
	in.mustRegister("filter", "Elements of l for which pred is true.", []Param{{Name: "pred", Type: "any"}, {Name: "l", Type: "list"}}, false, func(c *Call) (Value, error) {
		f, err := c.Proc(0)
		if err != nil {
			return Value{}, err
		}
		s, err := c.List(1)
		if err != nil {
			return Value{}, err
		}
		var out []Value
		for _, v := range s {
			r, err := c.In.Apply(c.Ctx, f, []Value{v})
			if err != nil {
				return Value{}, err
			}
			if r.IsTrue() {
				out = append(out, v)
			}
		}
		return List(out...), nil
	})
	in.mustRegister("fold", "Left fold: (f acc x) over l starting from init.", []Param{{Name: "f", Type: "any"}, {Name: "init", Type: "any"}, {Name: "l", Type: "list"}}, false, func(c *Call) (Value, error) {
		f, err := c.Proc(0)
		if err != nil {
			return Value{}, err
		}
		s, err := c.List(2)
		if err != nil {
			return Value{}, err
		}
		acc := c.Args[1]
		for _, v := range s {
			if acc, err = c.In.Apply(c.Ctx, f, []Value{acc, v}); err != nil {
				return Value{}, err
			}
		}
		return acc, nil
	})
	in.mustRegister("sort", "Stable sort of l by the less? procedure.", []Param{{Name: "l", Type: "list"}, {Name: "less?", Type: "any"}}, false, func(c *Call) (Value, error) {
		s, err := c.List(0)
		if err != nil {
			return Value{}, err
		}
		f, err := c.Proc(1)
		if err != nil {
			return Value{}, err
		}
		out := append([]Value(nil), s...)
		var sortErr error
		sort.SliceStable(out, func(i, j int) bool {
			if sortErr != nil {
				return false
			}
			r, err := c.In.Apply(c.Ctx, f, []Value{out[i], out[j]})
			if err != nil {
				sortErr = err
				return false
			}
			return r.IsTrue()
		})
		if sortErr != nil {
			return Value{}, sortErr
		}
		return List(out...), nil
	})
	in.mustRegister("apply", "Call f with the given arguments; the last argument must be a list.", []Param{{Name: "f", Type: "any"}, {Name: "l", Type: "list"}}, true, func(c *Call) (Value, error) {
		f, err := c.Proc(0)
		if err != nil {
			return Value{}, err
		}
		last, err := c.List(len(c.Args) - 1)
		if err != nil {
			return Value{}, err
		}
		args := append(append([]Value(nil), c.Args[1:len(c.Args)-1]...), last...)
		return c.In.Apply(c.Ctx, f, args)
	})
}

// applyAcross is map and for-each: it applies the procedure in argument 0
// to the elements of the lists after it, stopping at the shortest, and
// returns the results when keep is set.
func applyAcross(c *Call, keep bool) ([]Value, error) {
	f, err := c.Proc(0)
	if err != nil {
		return nil, err
	}
	lists := make([][]Value, len(c.Args)-1)
	n := -1
	for i := range lists {
		if lists[i], err = c.List(i + 1); err != nil {
			return nil, err
		}
		if n < 0 || len(lists[i]) < n {
			n = len(lists[i])
		}
	}
	var out []Value
	if keep {
		out = make([]Value, n)
	}
	for i := 0; i < n; i++ {
		args := make([]Value, len(lists))
		for j := range lists {
			args[j] = lists[j][i]
		}
		v, err := c.In.Apply(c.Ctx, f, args)
		if err != nil {
			return nil, err
		}
		if keep {
			out[i] = v
		}
	}
	return out, nil
}
