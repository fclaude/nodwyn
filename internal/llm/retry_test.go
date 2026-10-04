package llm

import (
	"context"
	"errors"
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
