package scheme

import (
	"io"
)

// mustRegister registers a builtin; a bad declaration is a programming
// error, so it panics at startup rather than failing later.
func (in *Interp) mustRegister(name, doc string, params []Param, variadic bool, h func(c *Call) (Value, error)) {
	in.mustRegisterBuiltin(name, doc, params, variadic, false, h)
}

// mustRegisterOpaque declares a measurement or predicate whose result does
// not carry its arguments' provenance (SPEC L2). Ordinary builtins propagate.
func (in *Interp) mustRegisterOpaque(name, doc string, params []Param, variadic bool, h func(c *Call) (Value, error)) {
	in.mustRegisterBuiltin(name, doc, params, variadic, true, h)
}

func (in *Interp) mustRegisterBuiltin(name, doc string, params []Param, variadic, opaque bool, h func(c *Call) (Value, error)) {
	err := in.Register(&Prim{
		Name:    name,
		Meta:    Meta{Doc: doc, Params: params, Variadic: variadic, Origin: "builtin", TaintOpaque: opaque},
		Handler: h,
	})
	if err != nil {
		panic(err)
	}
}

func (in *Interp) registerBuiltins() {
	in.registerNumbers()
	in.registerLists()
	in.registerStrings()
	in.registerVectors()
	in.registerCore()
	in.registerJSON()
	in.registerMeta()
	in.registerTaint()
}

func (in *Interp) registerCore() {
	any1 := []Param{{Name: "x", Type: "any"}}
	any2 := []Param{{Name: "a", Type: "any"}, {Name: "b", Type: "any"}}

	in.mustRegisterOpaque("not", "True only for #f.", any1, false, func(c *Call) (Value, error) {
		return Bool(!c.Args[0].IsTrue()), nil
	})
	in.mustRegisterOpaque("boolean?", "True for #t and #f.", any1, false, func(c *Call) (Value, error) {
		return Bool(c.Args[0].Kind == KBool), nil
	})
	in.mustRegisterOpaque("eq?", "Identity for pairs and procedures; value for everything else.", any2, false, func(c *Call) (Value, error) {
		return Bool(Eqv(c.Args[0], c.Args[1])), nil
	})
	in.mustRegisterOpaque("eqv?", "Alias of eq?.", any2, false, func(c *Call) (Value, error) {
		return Bool(Eqv(c.Args[0], c.Args[1])), nil
	})
	in.mustRegisterOpaque("equal?", "Structural equality.", any2, false, func(c *Call) (Value, error) {
		return Bool(Equal(c.Args[0], c.Args[1])), nil
	})
	in.mustRegisterOpaque("procedure?", "True for closures and primitives.", any1, false, func(c *Call) (Value, error) {
		return Bool(c.Args[0].IsProcedure()), nil
	})
	in.mustRegisterOpaque("handle?", "True for credential handles.", any1, false, func(c *Call) (Value, error) {
		return Bool(c.Args[0].Kind == KHandle), nil
	})
	in.mustRegister("void", "The void value.", nil, false, func(c *Call) (Value, error) {
		return Void, nil
	})
	in.mustRegister("error", "Raise an error with a message and irritants.", []Param{{Name: "message", Type: "string"}}, true, func(c *Call) (Value, error) {
		msg, err := c.String(0)
		if err != nil {
			return Value{}, err
		}
		return Value{}, &Error{Message: msg, Irritants: c.Args[1:]}
	})

	in.mustRegister("display", "Print x in human form.", any1, false, func(c *Call) (Value, error) {
		c.In.printed = Union(c.In.printed, DeepTaint(c.Args[0]))
		_, err := io.WriteString(c.In.out, Display(c.Args[0]))
		return Void, err
	})
	in.mustRegister("write", "Print x in machine form.", any1, false, func(c *Call) (Value, error) {
		c.In.printed = Union(c.In.printed, DeepTaint(c.Args[0]))
		_, err := io.WriteString(c.In.out, Write(c.Args[0]))
		return Void, err
	})
	in.mustRegister("newline", "Print a newline.", nil, false, func(c *Call) (Value, error) {
		_, err := io.WriteString(c.In.out, "\n")
		return Void, err
	})
}
