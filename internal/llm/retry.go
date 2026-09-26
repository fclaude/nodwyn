package llm

import (
	"context"
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

// ChatRetrying sends req to p, sending it again up to Retries times when it
// fails. Each attempt gets its own timeout from attemptCtx. onRetry, if not
// nil, is told about each failure that will be retried. A failure is not
// retried once ctx itself is done (cancelled, or out of time): that is the
// caller stopping, not the model failing.
func ChatRetrying(ctx context.Context, p Provider, req Request,
	attemptCtx func(context.Context) (context.Context, context.CancelFunc),
	onRetry func(attempt int, err error)) (*Response, error) {
	delay := RetryDelay
	for attempt := 0; ; attempt++ {
		actx, cancel := attemptCtx(ctx)
		resp, err := p.Chat(actx, req, nil)
		cancel()
		if err == nil || attempt == Retries || ctx.Err() != nil {
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
