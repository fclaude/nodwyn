package scheme

import "context"

// Origin says who initiated an evaluation: the user (REPL, init.scm, a
// front-end command) or the model (a tool call). The guard reads it from the
// context; L2 (P6) restricts taint-stripping to user-originated calls.
type Origin string

const (
	OriginUser    Origin = "user"
	OriginModel   Origin = "model"
	OriginHarness Origin = "harness" // the loop's own calls (the plan gate)
)

type originKey struct{}

// WithOrigin tags ctx with who initiated the evaluation.
func WithOrigin(ctx context.Context, o Origin) context.Context {
	return context.WithValue(ctx, originKey{}, o)
}

// OriginFrom returns the origin tagged on ctx; OriginUser if none.
func OriginFrom(ctx context.Context) Origin {
	if o, ok := ctx.Value(originKey{}).(Origin); ok {
		return o
	}
	return OriginUser
}
