package scheme

// Halt is an error a Scheme program may not catch: the harness has decided
// the work is over. Budget and progress stops raise it.
//
// It exists because orchestration now lives in Scheme. A strategy is
// ordinary code, and ordinary code can wrap anything in guard; a strategy
// that could catch a stop could run forever, and the budgets that bound an
// unattended run would be advisory. So a stop joins a guard denial and a
// cancelled context as the three things guard lets through untouched.
type Halt struct{ Err error }

func (h *Halt) Error() string { return h.Err.Error() }
func (h *Halt) Unwrap() error { return h.Err }
