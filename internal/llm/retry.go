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

// MaxRetryAfter is the longest wait a server's Retry-After is honoured for.
// A rate limit that asks for longer is reported at once instead, since a
// turn that sits silent for minutes looks hung; its failure says to wait.
var MaxRetryAfter = time.Minute

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
		start := time.Now()
		resp, err := p.Chat(actx, req, nil)
		if err == nil && resp != nil {
			// The request's wall time as this machine saw it, this attempt
			// alone: a retry's earlier failures are not the model's time.
			resp.Usage.WallMS = time.Since(start).Milliseconds()
		}
		cancel()
		var permanent permanentError
		if err == nil || attempt == Retries || ctx.Err() != nil || errors.As(err, &permanent) {
			return resp, err
		}
		// A server that says how long to wait is waited for: a rate limit
		// counts tokens over a minute, and two retries two and four seconds
		// apart land inside the same minute (39 of the 44 retried 429s in the
		// local sessions of 2026-09-30 to 10-05 were "tokens per minute", in
		// 20261004T182412Z-747045aa). The logs keep no response headers, so
		// whether that provider sends Retry-After is not known; one that does
		// is believed.
		wait := delay
		var se *StatusError
		if errors.As(err, &se) && se.RetryAfter > 0 {
			if se.RetryAfter > MaxRetryAfter {
				return resp, err
			}
			wait = max(se.RetryAfter, delay)
		}
		if onRetry != nil {
			onRetry(attempt+1, err)
		}
		select {
		case <-ctx.Done():
			return nil, err
		case <-time.After(wait):
		}
		delay *= 2
	}
}
