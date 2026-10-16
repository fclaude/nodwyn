package scheme

import (
	"math"
	"strconv"
)

func (in *Interp) registerNumbers() {
	n := func(name string) []Param { return []Param{{Name: name, Type: "number"}} }
	nn := []Param{{Name: "a", Type: "number"}, {Name: "b", Type: "number"}}
	ii := []Param{{Name: "n", Type: "integer"}, {Name: "m", Type: "integer"}}

	in.mustRegister("+", "Sum of the arguments; (+) is 0.", nil, true, func(c *Call) (Value, error) {
		return foldNum(c, Int(0), func(a, b Value) (Value, error) { return arith(a, b, '+') })
	})
	in.mustRegister("*", "Product of the arguments; (*) is 1.", nil, true, func(c *Call) (Value, error) {
		return foldNum(c, Int(1), func(a, b Value) (Value, error) { return arith(a, b, '*') })
	})
	in.mustRegister("-", "Subtract the rest from the first; (- n) negates.", n("n"), true, func(c *Call) (Value, error) {
		return foldDown(c, Int(0), '-')
	})
	in.mustRegister("/", "Divide the first by the rest; (/ n) inverts. Exact integer division stays an integer, otherwise a float.", n("n"), true, func(c *Call) (Value, error) {
		return foldDown(c, Int(1), '/')
	})
	cmp := func(name, doc string, ok func(a, b float64, ai, bi int64, ints bool) bool) {
		in.mustRegisterOpaque(name, doc, nn, true, func(c *Call) (Value, error) {
			for i := 0; i+1 < len(c.Args); i++ {
				a, err := c.Number(i)
				if err != nil {
					return Value{}, err
				}
				b, err := c.Number(i + 1)
				if err != nil {
					return Value{}, err
				}
				ints := a.Kind == KInt && b.Kind == KInt
				if !ok(a.AsFloat(), b.AsFloat(), a.I, b.I, ints) {
					return False, nil
				}
			}
			return True, nil
		})
	}
	cmp("=", "Numeric equality chain.", func(a, b float64, ai, bi int64, ints bool) bool {
		if ints {
			return ai == bi
		}
		return a == b
	})
	cmp("<", "Strictly increasing chain.", func(a, b float64, ai, bi int64, ints bool) bool {
		if ints {
			return ai < bi
		}
		return a < b
	})
	cmp(">", "Strictly decreasing chain.", func(a, b float64, ai, bi int64, ints bool) bool {
		if ints {
			return ai > bi
		}
		return a > b
	})
	cmp("<=", "Non-decreasing chain.", func(a, b float64, ai, bi int64, ints bool) bool {
		if ints {
			return ai <= bi
		}
		return a <= b
	})
	cmp(">=", "Non-increasing chain.", func(a, b float64, ai, bi int64, ints bool) bool {
		if ints {
			return ai >= bi
		}
		return a >= b
	})

	intDiv := func(name, doc string, f func(n, m int64) int64) {
		in.mustRegister(name, doc, ii, false, func(c *Call) (Value, error) {
			a, err := c.Int(0)
			if err != nil {
				return Value{}, err
			}
			b, err := c.Int(1)
			if err != nil {
				return Value{}, err
			}
			if b == 0 {
				return Value{}, errorf(c.Name, "division by zero")
			}
			return Int(f(a, b)), nil
		})
	}
	intDiv("quotient", "Integer division truncating toward zero.", func(n, m int64) int64 { return n / m })
	intDiv("remainder", "Remainder with the sign of the dividend.", func(n, m int64) int64 { return n % m })
	intDiv("modulo", "Remainder with the sign of the divisor.", func(n, m int64) int64 {
		r := n % m
		if r != 0 && (r < 0) != (m < 0) {
			r += m
		}
		return r
	})

	in.mustRegister("abs", "Absolute value.", n("n"), false, func(c *Call) (Value, error) {
		v, err := c.Number(0)
		if err != nil {
			return Value{}, err
		}
		if v.Kind == KInt {
			if v.I < 0 {
				return Int(-v.I), nil
			}
			return v, nil
		}
		return Float(math.Abs(v.F)), nil
	})
	minmax := func(name, doc string, pick func(a, b Value) bool) {
		in.mustRegister(name, doc, n("n"), true, func(c *Call) (Value, error) {
			best, err := c.Number(0)
			if err != nil {
				return Value{}, err
			}
			inexact := best.Kind == KFloat
			for i := 1; i < len(c.Args); i++ {
				v, err := c.Number(i)
				if err != nil {
					return Value{}, err
				}
				inexact = inexact || v.Kind == KFloat
				if pick(v, best) {
					best = v
				}
			}
			if inexact && best.Kind == KInt {
				return Float(float64(best.I)), nil
			}
			return best, nil
		})
	}
	minmax("min", "Smallest argument; float if any argument is a float.", func(a, b Value) bool { return a.AsFloat() < b.AsFloat() })
	minmax("max", "Largest argument; float if any argument is a float.", func(a, b Value) bool { return a.AsFloat() > b.AsFloat() })

	rounder := func(name, doc string, f func(float64) float64) {
		in.mustRegister(name, doc, n("x"), false, func(c *Call) (Value, error) {
			v, err := c.Number(0)
			if err != nil {
				return Value{}, err
			}
			if v.Kind == KInt {
				return v, nil
			}
			return Float(f(v.F)), nil
		})
	}
	rounder("floor", "Largest integral value not greater than x; integers pass through.", math.Floor)
	rounder("ceiling", "Smallest integral value not less than x; integers pass through.", math.Ceil)
	rounder("round", "Nearest integral value, half to even; integers pass through.", math.RoundToEven)
	rounder("truncate", "Integral value toward zero; integers pass through.", math.Trunc)

	in.mustRegister("exact", "Float to integer, truncating toward zero; integers pass through.", n("x"), false, func(c *Call) (Value, error) {
		v, err := c.Number(0)
		if err != nil {
			return Value{}, err
		}
		if v.Kind == KInt {
			return v, nil
		}
		if math.IsNaN(v.F) || math.IsInf(v.F, 0) || math.Abs(v.F) > math.MaxInt64 {
			return Value{}, errorf(c.Name, "cannot convert %s to an integer", Write(v))
		}
		return Int(int64(v.F)), nil
	})
	in.mustRegister("inexact", "Integer to float; floats pass through.", n("x"), false, func(c *Call) (Value, error) {
		v, err := c.Number(0)
		if err != nil {
			return Value{}, err
		}
		return Float(v.AsFloat()), nil
	})
	in.mustRegister("expt", "b raised to e. Two integers with e >= 0 give an integer, or an error when it does not fit 64 bits; otherwise a float.", []Param{{Name: "b", Type: "number"}, {Name: "e", Type: "number"}}, false, func(c *Call) (Value, error) {
		b, err := c.Number(0)
		if err != nil {
			return Value{}, err
		}
		e, err := c.Number(1)
		if err != nil {
			return Value{}, err
		}
		if b.Kind == KInt && e.Kind == KInt && e.I >= 0 {
			r, ok := intPow(b.I, e.I)
			if !ok {
				return Value{}, errorf(c.Name, "%d to the power %d does not fit an integer", b.I, e.I)
			}
			return Int(r), nil
		}
		return Float(math.Pow(b.AsFloat(), e.AsFloat())), nil
	})
	in.mustRegister("sqrt", "Square root, always a float.", n("x"), false, func(c *Call) (Value, error) {
		v, err := c.Number(0)
		if err != nil {
			return Value{}, err
		}
		return Float(math.Sqrt(v.AsFloat())), nil
	})

	pred := func(name, doc string, f func(v Value) bool) {
		in.mustRegisterOpaque(name, doc, []Param{{Name: "x", Type: "any"}}, false, func(c *Call) (Value, error) {
			return Bool(f(c.Args[0])), nil
		})
	}
	pred("number?", "True for integers and floats.", func(v Value) bool { return v.IsNumber() })
	pred("integer?", "True for integers.", func(v Value) bool { return v.Kind == KInt })
	pred("float?", "True for floats.", func(v Value) bool { return v.Kind == KFloat })

	numPred := func(name, doc string, f func(v Value) bool) {
		in.mustRegisterOpaque(name, doc, n("n"), false, func(c *Call) (Value, error) {
			v, err := c.Number(0)
			if err != nil {
				return Value{}, err
			}
			return Bool(f(v)), nil
		})
	}
	numPred("zero?", "True for 0 and 0.0.", func(v Value) bool { return v.AsFloat() == 0 })
	numPred("positive?", "True for numbers greater than zero.", func(v Value) bool { return v.AsFloat() > 0 })
	numPred("negative?", "True for numbers less than zero.", func(v Value) bool { return v.AsFloat() < 0 })
	intPred := func(name, doc string, f func(n int64) bool) {
		in.mustRegisterOpaque(name, doc, []Param{{Name: "n", Type: "integer"}}, false, func(c *Call) (Value, error) {
			v, err := c.Int(0)
			if err != nil {
				return Value{}, err
			}
			return Bool(f(v)), nil
		})
	}
	intPred("even?", "True for even integers.", func(n int64) bool { return n%2 == 0 })
	intPred("odd?", "True for odd integers.", func(n int64) bool { return n%2 != 0 })

	in.mustRegister("number->string", "Render a number the way write does.", n("n"), false, func(c *Call) (Value, error) {
		v, err := c.Number(0)
		if err != nil {
			return Value{}, err
		}
		return Str(Write(v)), nil
	})
	in.mustRegister("string->number", "Parse a number; #f if the string is not one.", []Param{{Name: "s", Type: "string"}}, false, func(c *Call) (Value, error) {
		s, err := c.String(0)
		if err != nil {
			return Value{}, err
		}
		if i, err := strconv.ParseInt(s, 10, 64); err == nil {
			return Int(i), nil
		}
		if f, err := strconv.ParseFloat(s, 64); err == nil && isPlainFloat(s) {
			return Float(f), nil
		}
		return False, nil
	})
}

func foldNum(c *Call, init Value, f func(a, b Value) (Value, error)) (Value, error) {
	acc := init
	for i := range c.Args {
		v, err := c.Number(i)
		if err != nil {
			return Value{}, err
		}
		if acc, err = f(acc, v); err != nil {
			return Value{}, err
		}
	}
	return acc, nil
}

func arith(a, b Value, op byte) (Value, error) {
	if a.Kind == KInt && b.Kind == KInt {
		switch op {
		case '+':
			return Int(a.I + b.I), nil
		case '-':
			return Int(a.I - b.I), nil
		case '*':
			return Int(a.I * b.I), nil
		case '/':
			if b.I == 0 {
				return Value{}, errorf("/", "division by zero")
			}
			if a.I%b.I == 0 {
				return Int(a.I / b.I), nil
			}
			return Float(float64(a.I) / float64(b.I)), nil
		}
	}
	x, y := a.AsFloat(), b.AsFloat()
	switch op {
	case '+':
		return Float(x + y), nil
	case '-':
		return Float(x - y), nil
	case '*':
		return Float(x * y), nil
	case '/':
		if y == 0 {
			return Value{}, errorf("/", "division by zero")
		}
		return Float(x / y), nil
	}
	return Value{}, errorf("arith", "bad op")
}

// intPow is b**e for e >= 0 by squaring, so that a huge exponent costs
// O(log e) and not a loop no abort can stop; ok is false when the result
// does not fit an int64 (the loop it replaces wrapped around silently).
func intPow(b, e int64) (r int64, ok bool) {
	r = 1
	for e > 0 {
		if e&1 == 1 {
			if r, ok = mulInt(r, b); !ok {
				return 0, false
			}
		}
		e >>= 1
		if e > 0 {
			if b, ok = mulInt(b, b); !ok {
				return 0, false
			}
		}
	}
	return r, true
}

// mulInt multiplies without wrapping.
func mulInt(a, b int64) (int64, bool) {
	if a == 0 || b == 0 {
		return 0, true
	}
	p := a * b
	if p/b != a || (a == -1 && b == math.MinInt64) || (b == -1 && a == math.MinInt64) {
		return 0, false
	}
	return p, true
}

// foldDown is - and /: the first argument less (or over) each of the rest in
// turn, and unit op the only argument when there is just one.
func foldDown(c *Call, unit Value, op byte) (Value, error) {
	acc, err := c.Number(0)
	if err != nil {
		return Value{}, err
	}
	if len(c.Args) == 1 {
		return arith(unit, acc, op)
	}
	for i := 1; i < len(c.Args); i++ {
		b, err := c.Number(i)
		if err != nil {
			return Value{}, err
		}
		if acc, err = arith(acc, b, op); err != nil {
			return Value{}, err
		}
	}
	return acc, nil
}
