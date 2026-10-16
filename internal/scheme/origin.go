package scheme

import "context"

// Origin says who initiated an evaluation: the user (REPL, init.scm, a
// front-end command) or the model (a tool call). The guard reads it from the
// context; L2 (P6) restricts taint-stripping to user-originated calls.
type Origin string

const (
	OriginUser    Origin = "user"
	OriginModel   Origin = "model"
	OriginHarness Origin = "harness" // the loop's strategy (agent-turn, the plan gate)
)

type originKey struct{}

// WithOrigin tags ctx with who initiated the evaluation.
func WithOrigin(ctx context.Context, o Origin) context.Context {
	return context.WithValue(ctx, originKey{}, o)
}

// OriginFrom returns the origin tagged on ctx; OriginHarness if none. The
// default is not the user's, which may strip provenance and change policy:
// an evaluation started where nobody said who started it (a new goroutine,
// a callback) gets no more than the loop's strategy has. The user's entry
// points (the REPL, init.scm, a console command) say so.
func OriginFrom(ctx context.Context) Origin {
	if o, ok := ctx.Value(originKey{}).(Origin); ok {
		return o
	}
	return OriginHarness
}
