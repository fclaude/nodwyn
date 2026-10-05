package llm

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

type flaky struct {
	failures  int
	calls     int
	err       error         // the failure to return; a transient parse error when nil
	failDelay time.Duration // how long a failing attempt takes
	okDelay   time.Duration // how long a successful attempt takes
}

func (f *flaky) Name() string                         { return "flaky" }
func (f *flaky) Ping(context.Context) (string, error) { return "", nil }
func (f *flaky) Chat(ctx context.Context, _ Request, _ func(Delta)) (*Response, error) {
	f.calls++
	if f.calls <= f.failures {
		time.Sleep(f.failDelay)
		if f.err != nil {
			return nil, f.err
		}
		return nil, errors.New("expected element type <function> but have <parameter>")
	}
	time.Sleep(f.okDelay)
	return &Response{Message: Message{Content: "ok"}}, nil
}

func withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, time.Second)
}

func TestChatRetryingRecoversFromTransientFailures(t *testing.T) {
	defer func(d time.Duration) { RetryDelay = d }(RetryDelay)
	RetryDelay = time.Millisecond
	p := &flaky{failures: Retries}
	var retried []int
	resp, err := ChatRetrying(context.Background(), p, Request{}, withTimeout,
		func(n int, _ error) { retried = append(retried, n) })
	if err != nil || resp.Message.Content != "ok" || p.calls != Retries+1 || len(retried) != Retries {
		t.Fatalf("resp=%v err=%v calls=%d retried=%v", resp, err, p.calls, retried)
	}
}

func TestChatRetryingGivesUpAfterRetries(t *testing.T) {
	defer func(d time.Duration) { RetryDelay = d }(RetryDelay)
	RetryDelay = time.Millisecond
	p := &flaky{failures: Retries + 5}
	if _, err := ChatRetrying(context.Background(), p, Request{}, withTimeout, nil); err == nil || p.calls != Retries+1 {
		t.Fatalf("err=%v calls=%d", err, p.calls)
	}
}

func TestChatRetryingStopsWhenCallerIsDone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := &flaky{failures: 1}
	if _, err := ChatRetrying(ctx, p, Request{}, withTimeout, nil); err == nil || p.calls != 1 {
		t.Fatalf("err=%v calls=%d", err, p.calls)
	}
}

// A permanent failure, such as a model nobody configured, is not sent
// again: the same request cannot succeed.
func TestChatRetryingStopsAtPermanentFailures(t *testing.T) {
	defer func(d time.Duration) { RetryDelay = d }(RetryDelay)
	RetryDelay = time.Millisecond
	p := &flaky{failures: 5, err: Permanent(errors.New("no control model is configured"))}
	_, err := ChatRetrying(context.Background(), p, Request{}, withTimeout, func(int, error) { t.Fatal("retried a permanent failure") })
	if err == nil || p.calls != 1 {
		t.Fatalf("calls %d, err %v", p.calls, err)
	}
}

// The wall time ChatRetrying reports is the successful attempt's alone: a
// retry's earlier failures are not the model's time, and tok/s reads it.
func TestChatRetryingTimesTheSuccessfulAttempt(t *testing.T) {
	defer func(d time.Duration) { RetryDelay = d }(RetryDelay)
	RetryDelay = time.Millisecond
	p := &flaky{failures: 1, failDelay: 50 * time.Millisecond, okDelay: 2 * time.Millisecond}
	resp, err := ChatRetrying(context.Background(), p, Request{}, withTimeout, nil)
	if err != nil || resp.Message.Content != "ok" {
		t.Fatalf("resp=%v err=%v", resp, err)
	}
	if resp.Usage.WallMS < 2 || resp.Usage.WallMS >= 50 {
		t.Fatalf("wall_ms %d, want the successful attempt's few ms, not the failed one's 50", resp.Usage.WallMS)
	}
}

// A server that says how long to wait is waited for, not asked again two
// seconds later; a wait longer than MaxRetryAfter ends the request at once
// with the server's own words, and an account out of credit (402) is not
// retried at all.
func TestChatRetryingWaitsForRetryAfterAndStopsAtPaymentRequired(t *testing.T) {
	defer func(d, m time.Duration) { RetryDelay, MaxRetryAfter = d, m }(RetryDelay, MaxRetryAfter)
	RetryDelay, MaxRetryAfter = time.Millisecond, time.Second

	limited := func(after time.Duration) error {
		return &StatusError{Provider: "openai", Status: 429, Text: "429 Too Many Requests", Body: "tokens per minute", RetryAfter: after}
	}
	p := &flaky{failures: 1, err: limited(60 * time.Millisecond)}
	start := time.Now()
	if _, err := ChatRetrying(context.Background(), p, Request{}, withTimeout, nil); err != nil || p.calls != 2 {
		t.Fatalf("calls %d, err %v", p.calls, err)
	}
	if waited := time.Since(start); waited < 60*time.Millisecond {
		t.Fatalf("asked again after %v, before the 60 ms the server asked for", waited)
	}

	p = &flaky{failures: 5, err: limited(time.Hour)}
	if _, err := ChatRetrying(context.Background(), p, Request{}, withTimeout, nil); err == nil || p.calls != 1 {
		t.Fatalf("a wait of an hour: calls %d, err %v", p.calls, err)
	}

	resp := &http.Response{StatusCode: 402, Status: "402 Payment Required", Header: http.Header{}}
	p = &flaky{failures: 5, err: refused("openai", resp, `{"code":"payment_required"}`)}
	if _, err := ChatRetrying(context.Background(), p, Request{}, withTimeout, func(int, error) { t.Fatal("retried a 402") }); err == nil || p.calls != 1 || !strings.Contains(Hint(err), "out of credit") {
		t.Fatalf("a 402: calls %d, err %v, hint %q", p.calls, err, Hint(err))
	}
}

func TestRetryAfterReadsSecondsMillisecondsAndDates(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		header map[string]string
		want   time.Duration
	}{
		{map[string]string{"Retry-After": "7"}, 7 * time.Second},
		{map[string]string{"Retry-After": "1.5"}, 1500 * time.Millisecond},
		{map[string]string{"Retry-After": now.Add(20 * time.Second).Format(http.TimeFormat)}, 20 * time.Second},
		{map[string]string{"Retry-After": "7", "Retry-After-Ms": "250"}, 250 * time.Millisecond},
		{map[string]string{"Retry-After": "soon"}, 0},
		{map[string]string{"Retry-After": now.Add(-time.Minute).Format(http.TimeFormat)}, 0},
		{nil, 0},
	} {
		h := http.Header{}
		for k, v := range c.header {
			h.Set(k, v)
		}
		if got := retryAfter(h, now); got != c.want {
			t.Errorf("%v: %v, want %v", c.header, got, c.want)
		}
	}
}
