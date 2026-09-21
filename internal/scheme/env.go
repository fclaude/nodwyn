package scheme

// Env is a lexical environment: a frame of bindings and a parent.
type Env struct {
	vars   map[string]Value
	parent *Env
}

// NewEnv creates an empty frame under parent (nil for the global frame).
func NewEnv(parent *Env) *Env {
	return &Env{vars: make(map[string]Value), parent: parent}
}

// Lookup finds name in this frame or any ancestor.
func (e *Env) Lookup(name string) (Value, bool) {
	for env := e; env != nil; env = env.parent {
		if v, ok := env.vars[name]; ok {
			return v, true
		}
	}
	return Value{}, false
}

// Define binds name in this frame, shadowing any ancestor binding.
func (e *Env) Define(name string, v Value) {
	e.vars[name] = v
}

// Set assigns an existing binding in the nearest frame that has it.
func (e *Env) Set(name string, v Value) bool {
	for env := e; env != nil; env = env.parent {
		if _, ok := env.vars[name]; ok {
			env.vars[name] = v
			return true
		}
	}
	return false
}
