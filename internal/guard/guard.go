// Package guard is the one wrapper at the Scheme→Go boundary (SPEC
// invariant 4). It is installed on the interpreter as its Gate and sees
// every primitive application with its full arguments and origin.
//
// A Guard is a pipeline of Checkers. Each returns a Decision; the strictest
// wins (deny > confirm > allow). A confirm is put to the user through the
// Confirmer, which the core implements as an event plus a wait on Answer —
// the guard never knows which front-end is attached (ADR 10). Every decision
// is logged.
package guard

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/fclaude/nibb/internal/log"
	"github.com/fclaude/nibb/internal/scheme"
)

// Verdict is the outcome class of a Decision.
type Verdict int

const (
	Allow Verdict = iota
	Confirm
	Deny
)

func (v Verdict) String() string {
	switch v {
	case Allow:
		return "allow"
	case Confirm:
		return "confirm"
	case Deny:
		return "deny"
	}
	return "?"
}

// Decision is a checker's answer. Reason is one line for the log and the
// user; Detail is optional longer context shown in a confirm prompt (a diff,
// a command line). Standing marks an Allow backed by a standing user
// approval: it outranks any Confirm (the user already said yes), never a
// Deny.
type Decision struct {
	Verdict  Verdict
	Reason   string
	Detail   string
	Standing bool
	// Security marks a Confirm that a standing approval must not waive: the
	// user has to see it every time (an L1 finding in a write, for example).
	Security bool
}

// Checker inspects one primitive application.
type Checker interface {
	Name() string
	Check(ctx context.Context, p *scheme.Prim, args []scheme.Value) Decision
}

// Prompt is what the user is asked when a checker wants confirmation.
// Argv is set for command lines so a front-end can offer a standing
// approval ("a" = always this session).
type Prompt struct {
	Security bool // standing and turn approvals must never waive this confirmation
	Prim     string
	Args     string // short written form
	Reasons  []string
	Detail   string
	Argv     []string // set for command lines
	Path     string   // set for writes (repository-relative)
}

// Confirmer answers prompts. It may block; it must honour ctx.
type Confirmer interface {
	Confirm(ctx context.Context, p Prompt) (bool, error)
}

// ConfirmFunc adapts a function to Confirmer.
type ConfirmFunc func(ctx context.Context, p Prompt) (bool, error)

func (f ConfirmFunc) Confirm(ctx context.Context, p Prompt) (bool, error) { return f(ctx, p) }

// DenyAll is the Confirmer for non-interactive runs: every confirm is a no.
var DenyAll = ConfirmFunc(func(context.Context, Prompt) (bool, error) { return false, nil })

// Logger receives guard decisions. The core session satisfies it (so the
// decision also reaches the front-end); a bare *log.Writer does not, on
// purpose: a decision the user cannot see is a decision half-logged.
type Logger interface {
	Record(kind log.Kind, payload any) (log.Event, error)
}

// Guard implements scheme.Gate.
type Guard struct {
	checkers  []Checker
	confirmer Confirmer
	logger    Logger
	// ev applies a Scheme procedure to arguments; used by RegisterChecker to
	// run a Scheme-registered checker. Set by the core (which owns the
	// interpreter). nil means no Scheme checkers are possible.
	ev SchemeEval
}

// New builds a guard. A nil confirmer denies every confirm; a nil logger
// discards decisions (tests only — nibb always logs).
func New(confirmer Confirmer, logger Logger, checkers ...Checker) *Guard {
	if confirmer == nil {
		confirmer = DenyAll
	}
	return &Guard{checkers: checkers, confirmer: confirmer, logger: logger}
}

// Add appends a checker to the pipeline. Checkers run in append order; the
// strictest verdict still wins, so order does not relax a deny.
func (g *Guard) Add(c Checker) { g.checkers = append(g.checkers, c) }

// SchemeEval is set by the core to a function that applies a Scheme
// procedure. Required for RegisterChecker.
type SchemeEval func(ctx context.Context, proc scheme.Value, args []scheme.Value) (scheme.Value, error)

// WithSchemeEval installs the evaluator RegisterChecker uses to run a
// Scheme procedure.
func (g *Guard) WithSchemeEval(ev SchemeEval) *Guard { g.ev = ev; return g }

// RegisterChecker implements scheme.Gate (register-check, ADR 20). It adds
// a tightening-only checker named name. The predicate is a Scheme closure
// (prim-name args) → boolean; when it returns #t the checker returns the
// fixed verdict (verdict must be "deny" or "confirm"); otherwise it returns
// Allow (no opinion). The strictest-wins merge guarantees a user checker can
// only tighten, never relax.
func (g *Guard) RegisterChecker(name, verdict string, predicate scheme.Value) error {
	if g.ev == nil {
		return errors.New("guard: no Scheme evaluator installed (WithSchemeEval)")
	}
	v := strings.ToLower(strings.TrimSpace(verdict))
	var d Decision
	switch v {
	case "deny":
		d = Decision{Verdict: Deny, Reason: "user checker " + name}
	case "confirm":
		d = Decision{Verdict: Confirm, Reason: "user checker " + name}
	default:
		return fmt.Errorf("register-check: verdict must be \"deny\" or \"confirm\", got %q", verdict)
	}
	g.Add(&schemeChecker{name: name, predicate: predicate, decision: d, ev: g.ev})
	return nil
}

type schemeChecker struct {
	name      string
	predicate scheme.Value // (prim-name args) → boolean
	decision  Decision     // returned when predicate is true
	ev        SchemeEval
}

// schemeEvalDepth is a context key to prevent re-entrant scheme checker
// evaluation (the predicate itself may call prims that go through the guard).
type schemeEvalDepth int

func (sc *schemeChecker) Name() string { return sc.name }

func (sc *schemeChecker) Check(ctx context.Context, p *scheme.Prim, args []scheme.Value) Decision {
	if d, _ := ctx.Value(schemeEvalDepth(0)).(schemeEvalDepth); d > 0 {
		// Already inside a predicate eval; do not re-enter.
		return Decision{Verdict: Allow, Reason: fmt.Sprintf("checker %q: re-entrant", sc.name)}
	}
	ctx = context.WithValue(ctx, schemeEvalDepth(0), schemeEvalDepth(1))
	out, err := sc.ev(ctx, sc.predicate, []scheme.Value{scheme.Str(p.Name), scheme.List(args...)})
	if err != nil {
		// A checker that errors is a policy error; fail closed.
		return Decision{Verdict: Deny, Reason: fmt.Sprintf("checker %q errored: %v", sc.name, err)}
	}
	if out.Kind == scheme.KBool && out.I != 0 {
		return sc.decision
	}
	return Decision{Verdict: Allow, Reason: fmt.Sprintf("checker %q: no match", sc.name)}
}

// SetConfirmer replaces the confirmer. The core installs DenyAll while
// init.scm loads (nobody is listening yet) and the real one afterwards, on
// the same guard, so checkers registered during init are kept.
func (g *Guard) SetConfirmer(c Confirmer) {
	if c == nil {
		c = DenyAll
	}
	g.confirmer = c
}

// checkResult keeps the policy verdict and its explanation together.
type checkResult struct {
	decision Decision
	checker  string
	reasons  []string
	details  []string
	standing string
	security bool
}

// evaluate runs in registration order and stops at the first denial.
// Standing approval only overrides ordinary confirmations.
func (g *Guard) evaluate(ctx context.Context, p *scheme.Prim, args []scheme.Value) checkResult {
	r := checkResult{decision: Decision{Verdict: Allow}}
	for _, c := range g.checkers {
		d := c.Check(ctx, p, args)
		if d.Verdict == Allow {
			if d.Standing && r.standing == "" {
				r.standing = d.Reason
			}
			continue
		}
		if d.Verdict == Confirm && d.Security {
			r.security = true
		}
		r.reasons = append(r.reasons, d.Reason)
		if d.Detail != "" {
			r.details = append(r.details, d.Detail)
		}
		if d.Verdict > r.decision.Verdict {
			r.decision = d
			r.checker = c.Name()
		}
		if d.Verdict == Deny {
			break // nothing can relax a deny
		}
	}
	if r.decision.Verdict == Confirm && r.standing != "" && !r.security {
		// The user approved this ahead of time (a command prefix or a write
		// path). The detail — a diff — goes to the log since nobody saw it.
		r.decision = Decision{Verdict: Allow, Standing: true, Reason: r.standing}
		r.reasons = []string{r.standing}
		r.checker = "standing"
	}
	return r
}

// Before is the Gate hook: it runs the checkers, asks the user if needed,
// logs, and returns nil (allow) or a *scheme.Denied.
func (g *Guard) Before(ctx context.Context, p *scheme.Prim, args []scheme.Value) error {
	origin := string(scheme.OriginFrom(ctx))
	result := g.evaluate(ctx, p, args)
	final := result.decision
	detail := strings.Join(result.details, "\n\n")
	rec := log.GuardDecision{
		Prim:     p.Name,
		Origin:   origin,
		Decision: final.Verdict.String(),
		Checker:  result.checker,
		Reason:   strings.Join(result.reasons, "; "),
		Args:     summarizeArgs(args),
	}
	if final.Standing {
		rec.Detail = detail
	}
	switch final.Verdict {
	case Allow:
		// Pure builtins (car, +, string-append ...) are allowed silently; a
		// log line per arithmetic op would bury the decisions that matter.
		// Anything that reaches the host, and every non-allow, is recorded.
		if p.Tool() || p.Meta.Host || p.Meta.Egress || p.Meta.Irreversible || p.Meta.Audited {
			if recordErr := g.record(rec); recordErr != nil {
				return recordErr
			}
		}
		return nil
	case Deny:
		if recordErr := g.record(rec); recordErr != nil {
			return recordErr
		}
		return &scheme.Denied{Prim: p.Name, Reason: final.Reason}
	}
	// Confirm.
	writePath, _ := WritePath(p, args)
	approval, err := g.confirm(ctx, Prompt{
		Security: result.security,
		Prim:     p.Name,
		Args:     rec.Args,
		Reasons:  result.reasons,
		Detail:   detail,
		Argv:     CommandArgs(p, args),
		Path:     writePath,
	})
	rec.Actor, rec.PromptID, rec.Scope = approval.Actor, approval.PromptID, approval.Scope
	if err != nil {
		rec.Answer = "error"
		if recordErr := g.record(rec); recordErr != nil {
			return recordErr
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return &scheme.Denied{Prim: p.Name, Reason: "confirmation failed: " + err.Error()}
	}
	if approval.Allowed {
		rec.Answer = "yes"
		if recordErr := g.record(rec); recordErr != nil {
			return recordErr
		}
		return nil
	}
	rec.Answer = "no"
	if recordErr := g.record(rec); recordErr != nil {
		return recordErr
	}
	return &scheme.Denied{Prim: p.Name, Reason: "not confirmed by the user"}
}

func (g *Guard) record(rec log.GuardDecision) error {
	if g.logger != nil {
		_, err := g.logger.Record(log.KindGuardDecision, rec)
		return err
	}
	return nil
}

// summarizeArgs renders arguments for the log, truncating long strings so a
// file's contents do not end up in a guard record.
func summarizeArgs(args []scheme.Value) string {
	parts := make([]string, len(args))
	for i, a := range args {
		s := scheme.Write(a)
		if len(s) > 120 {
			s = s[:117] + "..."
		}
		parts[i] = s
	}
	return fmt.Sprintf("(%s)", strings.Join(parts, " "))
}
