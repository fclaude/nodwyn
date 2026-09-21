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
	Prim    string
	Args    string // short written form
	Reasons []string
	Detail  string
	Argv    []string // set for command lines
	Path    string   // set for writes (repository-relative)
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
}

// New builds a guard. A nil confirmer denies every confirm; a nil logger
// discards decisions (tests only — nibb always logs).
func New(confirmer Confirmer, logger Logger, checkers ...Checker) *Guard {
	if confirmer == nil {
		confirmer = DenyAll
	}
	return &Guard{checkers: checkers, confirmer: confirmer, logger: logger}
}

// Before is the Gate hook: it runs the checkers, asks the user if needed,
// logs, and returns nil (allow) or a *scheme.Denied.
func (g *Guard) Before(ctx context.Context, p *scheme.Prim, args []scheme.Value) error {
	origin := string(scheme.OriginFrom(ctx))
	final := Decision{Verdict: Allow}
	var reasons []string
	var details []string
	checker := ""
	standing := ""
	for _, c := range g.checkers {
		d := c.Check(ctx, p, args)
		if d.Verdict == Allow {
			if d.Standing && standing == "" {
				standing = d.Reason
			}
			continue
		}
		reasons = append(reasons, d.Reason)
		if d.Detail != "" {
			details = append(details, d.Detail)
		}
		if d.Verdict > final.Verdict {
			final = d
			checker = c.Name()
		}
		if d.Verdict == Deny {
			break // nothing can relax a deny
		}
	}
	detail := strings.Join(details, "\n\n")
	if final.Verdict == Confirm && standing != "" {
		// The user approved this ahead of time (a command prefix or a write
		// path). The detail — a diff — goes to the log since nobody saw it.
		final = Decision{Verdict: Allow, Standing: true, Reason: standing}
		reasons = []string{standing}
		checker = "standing"
	}
	rec := log.GuardDecision{
		Prim:     p.Name,
		Origin:   origin,
		Decision: final.Verdict.String(),
		Checker:  checker,
		Reason:   strings.Join(reasons, "; "),
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
		if p.Tool() || p.Meta.Host || p.Meta.Egress || p.Meta.Irreversible {
			g.record(rec)
		}
		return nil
	case Deny:
		g.record(rec)
		return &scheme.Denied{Prim: p.Name, Reason: final.Reason}
	}
	// Confirm.
	writePath, _ := WritePath(p, args)
	ok, err := g.confirmer.Confirm(ctx, Prompt{
		Prim:    p.Name,
		Args:    rec.Args,
		Reasons: reasons,
		Detail:  detail,
		Argv:    argvArgs(p, args),
		Path:    writePath,
	})
	if err != nil {
		rec.Answer = "error"
		g.record(rec)
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return &scheme.Denied{Prim: p.Name, Reason: "confirmation failed: " + err.Error()}
	}
	if ok {
		rec.Answer = "yes"
		g.record(rec)
		return nil
	}
	rec.Answer = "no"
	g.record(rec)
	return &scheme.Denied{Prim: p.Name, Reason: "not confirmed by the user"}
}

func (g *Guard) record(rec log.GuardDecision) {
	if g.logger != nil {
		_, _ = g.logger.Record(log.KindGuardDecision, rec)
	}
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
