package llm

import (
	"context"
	"errors"
	"time"
)

// Retries is how many times a failed model request is sent again. A local
// model occasionally emits output its server cannot parse (a tool-call tag
// without its function, say) or the server drops a request under load;
// sampled again, the same request almost always succeeds. Without a retry
// one such sample ends the whole turn.
const Retries = 2

// RetryDelay is the pause before the first retry; it doubles after that.
var RetryDelay = 2 * time.Second

// Permanent marks an error that sending the request again cannot fix, such
// as a model nobody configured or a key that is not bound: ChatRetrying
// returns it at once.
func Permanent(err error) error { return permanentError{err} }

type permanentError struct{ error }

func (p permanentError) Unwrap() error { return p.error }

// ChatRetrying sends req to p, sending it again up to Retries times when it
// fails. Each attempt gets its own timeout from attemptCtx. onRetry, if not
// nil, is told about each failure that will be retried. A failure is not
// retried once ctx itself is done (cancelled, or out of time): that is the
// caller stopping, not the model failing. Nor is a Permanent one.
func ChatRetrying(ctx context.Context, p Provider, req Request,
	attemptCtx func(context.Context) (context.Context, context.CancelFunc),
	onRetry func(attempt int, err error)) (*Response, error) {
	delay := RetryDelay
	for attempt := 0; ; attempt++ {
		actx, cancel := attemptCtx(ctx)
		resp, err := p.Chat(actx, req, nil)
		cancel()
		var permanent permanentError
		if err == nil || attempt == Retries || ctx.Err() != nil || errors.As(err, &permanent) {
			return resp, err
		}
		if onRetry != nil {
			onRetry(attempt+1, err)
		}
		select {
		case <-ctx.Done():
			return nil, err
		case <-time.After(delay):
		}
		delay *= 2
	}
}
