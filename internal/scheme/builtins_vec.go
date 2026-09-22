package scheme

func (in *Interp) registerVectors() {
	any1 := []Param{{Name: "x", Type: "any"}}
	v := []Param{{Name: "v", Type: "vector"}}

	in.mustRegister("vector", "Build a vector from its arguments.", nil, true, func(c *Call) (Value, error) {
		return Vec(c.Args...), nil
	})
	in.mustRegisterOpaque("vector?", "True for vectors.", any1, false, func(c *Call) (Value, error) {
		return Bool(c.Args[0].Kind == KVec), nil
	})
	in.mustRegisterOpaque("vector-length", "Number of elements in a vector.", v, false, func(c *Call) (Value, error) {
		if c.Args[0].Kind != KVec {
			return Value{}, c.typeErr(0, "vector")
		}
		return Int(int64(len(c.Args[0].V.Items))), nil
	})
	in.mustRegister("vector-ref", "Element at 0-based index i of a vector.", []Param{{Name: "v", Type: "vector"}, {Name: "i", Type: "integer"}}, false, func(c *Call) (Value, error) {
		if c.Args[0].Kind != KVec {
			return Value{}, c.typeErr(0, "vector")
		}
		i, err := c.Int(1)
		if err != nil {
			return Value{}, err
		}
		if i < 0 || i >= int64(len(c.Args[0].V.Items)) {
			return Value{}, errorf(c.Name, "index %d out of range for vector of length %d", i, len(c.Args[0].V.Items))
		}
		return c.Args[0].V.Items[i], nil
	})
	in.mustRegister("vector-set!", "Set element i of a vector to x.", []Param{{Name: "v", Type: "vector"}, {Name: "i", Type: "integer"}, {Name: "x", Type: "any"}}, false, func(c *Call) (Value, error) {
		if c.Args[0].Kind != KVec {
			return Value{}, c.typeErr(0, "vector")
		}
		i, err := c.Int(1)
		if err != nil {
			return Value{}, err
		}
		if i < 0 || i >= int64(len(c.Args[0].V.Items)) {
			return Value{}, errorf(c.Name, "index %d out of range for vector of length %d", i, len(c.Args[0].V.Items))
		}
		c.Args[0].V.Items[i] = c.Args[2]
		return Void, nil
	})
	in.mustRegister("vector->list", "Elements of a vector as a list.", v, false, func(c *Call) (Value, error) {
		if c.Args[0].Kind != KVec {
			return Value{}, c.typeErr(0, "vector")
		}
		return List(c.Args[0].V.Items...), nil
	})
	in.mustRegister("list->vector", "A vector with the elements of a list.", []Param{{Name: "l", Type: "list"}}, false, func(c *Call) (Value, error) {
		s, err := c.List(0)
		if err != nil {
			return Value{}, err
		}
		return Vec(s...), nil
	})
}
