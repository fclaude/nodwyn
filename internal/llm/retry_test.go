package llm

import (
	"context"
	"errors"
	"testing"
	"time"
)

type flaky struct {
	failures int
	calls    int
}

func (f *flaky) Name() string                         { return "flaky" }
func (f *flaky) Ping(context.Context) (string, error) { return "", nil }
func (f *flaky) Chat(ctx context.Context, _ Request, _ func(Delta)) (*Response, error) {
	f.calls++
	if f.calls <= f.failures {
		return nil, errors.New("expected element type <function> but have <parameter>")
	}
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
