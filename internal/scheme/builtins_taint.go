package scheme

// Taint builtins (L2, Phase 6). Provenance is readable by anyone and
// strippable only by the user: `wipe` and its synonym `box` are marked
// TaintStrip, which the guard refuses from a model-originated call. That is
// the documented escape hatch of SPEC Risk 1 — L2 stops the model, not the
// user, who is the principal.

func (in *Interp) registerTaint() {
	in.mustRegisterOpaque("taint-of", "List the provenance tags a value carries, e.g. (\"external:file\"). An untainted value gives ().",
		[]Param{{Name: "x", Type: "any"}}, false, func(c *Call) (Value, error) {
			names := c.Args[0].Taint.Tags()
			out := make([]Value, len(names))
			for i, n := range names {
				out[i] = Str(n)
			}
			return List(out...), nil
		})
	in.mustRegisterOpaque("tainted?", "True if a value carries any provenance tag.",
		[]Param{{Name: "x", Type: "any"}}, false, func(c *Call) (Value, error) {
			return Bool(!c.Args[0].Taint.IsClean()), nil
		})

	strip := func(name, doc string) {
		p := &Prim{
			Name: name,
			Meta: Meta{
				Doc:        doc,
				Params:     []Param{{Name: "x", Type: "any"}},
				Hidden:     true, // never offered to the model
				Audited:    true, // the decision is logged even when allowed
				TaintStrip: true, // the guard refuses a model-originated call
				Origin:     "builtin",
			},
			Handler: func(c *Call) (Value, error) { return c.Args[0], nil },
		}
		if err := in.Register(p); err != nil {
			panic(err)
		}
	}
	strip("wipe", "Return the value with its provenance tags removed. User-originated Scheme only; every use is logged.")
}
