package loop

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fclaude/brote/scheme"
	"github.com/fclaude/nodwyn/internal/budget"
	"github.com/fclaude/nodwyn/internal/event"
	"github.com/fclaude/nodwyn/internal/llm"
	"github.com/fclaude/nodwyn/internal/log"
)

// batchHost keeps what the loop records, in the order it is recorded.
type batchHost struct {
	mu     sync.Mutex
	events []batchEvent
}

type batchEvent struct {
	kind    log.Kind
	payload any
}

func (h *batchHost) Record(kind log.Kind, payload any) (log.Event, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.events = append(h.events, batchEvent{kind, payload})
	return log.Event{Seq: uint64(len(h.events)), Kind: kind}, nil
}
func (h *batchHost) EventsSoFar() []log.Event { return nil }
func (h *batchHost) Emit(event.Event)         {}

// ids lists the ids of the recorded events of kind, in the order recorded.
func (h *batchHost) ids(kind log.Kind) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, ev := range h.events {
		switch p := ev.payload.(type) {
		case log.ToolCallEvent:
			if ev.kind == kind {
				out = append(out, p.ID)
			}
		case log.ToolResult:
			if ev.kind == kind {
				out = append(out, p.ID)
			}
		}
	}
	return out
}

// result is the recorded result of the call with id.
func (h *batchHost) result(t *testing.T, id string) log.ToolResult {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, ev := range h.events {
		if p, ok := ev.payload.(log.ToolResult); ok && p.ID == id {
			return p
		}
	}
	t.Fatalf("no result recorded for call %s", id)
	return log.ToolResult{}
}

func (h *batchHost) count(kind log.Kind) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, ev := range h.events {
		if ev.kind == kind {
			n++
		}
	}
	return n
}

// probe is a tool that says how many of its calls ran at the same time.
// Calls whose path is in hold wait for release.
type probe struct {
	mu      sync.Mutex
	active  int
	most    int
	entered int
	enough  chan struct{} // closed once want calls are inside at once
	want    int
	work    func(path string, c *scheme.Call) error
}

// enter is a call of the tool. With want calls inside together (the batch ran
// concurrently), every call is let go; a call alone for too long says so.
func (p *probe) enter(path string, c *scheme.Call) (scheme.Value, error) {
	p.mu.Lock()
	p.active++
	p.entered++
	p.most = max(p.most, p.active)
	if p.want > 0 && p.active == p.want {
		close(p.enough)
	}
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		p.active--
		p.mu.Unlock()
	}()
	if p.want > 0 {
		select {
		case <-p.enough:
		case <-time.After(3 * time.Second):
			return scheme.Void, errors.New("no other call began while this one ran")
		}
	}
	if p.work != nil {
		if err := p.work(path, c); err != nil {
			return scheme.Void, err
		}
	}
	return scheme.Str("read " + path), nil
}

func (p *probe) overlap() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.most
}

// batchRig is a loop with the tools a test names, each one the probe.
type batchRig struct {
	l     *Loop
	host  *batchHost
	probe *probe
}

func newBatchRig(t *testing.T, want int, tools ...string) *batchRig {
	t.Helper()
	p := &probe{want: want, enough: make(chan struct{})}
	in := scheme.New()
	for _, name := range tools {
		err := in.Register(&scheme.Prim{
			Name: name,
			Meta: scheme.Meta{Doc: name + " for a test", Host: true, Params: []scheme.Param{{Name: "path", Type: "string", Doc: "a path"}}},
			Handler: func(c *scheme.Call) (scheme.Value, error) {
				path, err := c.String(0)
				if err != nil {
					return scheme.Void, err
				}
				return p.enter(path, c)
			},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	host := &batchHost{}
	return &batchRig{l: &Loop{host: host, interp: in, turn: &turnState{}}, host: host, probe: p}
}

func pathCall(id, name, path string) llm.ToolCall {
	return llm.ToolCall{ID: id, Name: name, Args: []byte(fmt.Sprintf(`{"path":%q}`, path))}
}

// run is the strategy's (run-tools calls), after the model answered with them.
func (r *batchRig) run(ctx context.Context, calls ...llm.ToolCall) (scheme.Value, error) {
	r.l.turn.expect(calls)
	items := make([]scheme.Value, len(calls))
	for i, tc := range calls {
		items[i] = callValue(tc)
	}
	return r.l.runTools(&scheme.Call{Ctx: ctx, Args: []scheme.Value{scheme.List(items...)}, Origin: scheme.OriginHarness})
}

// outcomes reads a run-tools answer as "id:ok" or "id:failed" per call.
func outcomes(t *testing.T, v scheme.Value) []string {
	t.Helper()
	items, ok := v.Slice()
	if !ok {
		t.Fatalf("not a list: %s", scheme.Write(v))
	}
	var out []string
	for _, item := range items {
		ok, _ := lookup(item, "ok")
		state := "failed"
		if ok.IsTrue() {
			state = "ok"
		}
		out = append(out, field(item, "id")+":"+state)
	}
	return out
}

func sameList(got, want []string) bool { return strings.Join(got, " ") == strings.Join(want, " ") }

func TestReadOnlyBatchRunsConcurrently(t *testing.T) {
	r := newBatchRig(t, 2, "fs-read", "fs-list")
	v, err := r.run(context.Background(), pathCall("a", "fs-read", "x.go"), pathCall("b", "fs-list", "."))
	if err != nil {
		t.Fatal(err)
	}
	if got := outcomes(t, v); !sameList(got, []string{"a:ok", "b:ok"}) {
		t.Fatalf("results %v: the second call never began while the first was running", got)
	}
}

func TestConcurrentBatchKeepsCallOrder(t *testing.T) {
	r := newBatchRig(t, 3, "fs-read")
	// The first call ends last, the third first.
	delays := map[string]time.Duration{"a": 80 * time.Millisecond, "b": 40 * time.Millisecond, "c": 0}
	r.probe.work = func(path string, _ *scheme.Call) error { time.Sleep(delays[path]); return nil }
	v, err := r.run(context.Background(), pathCall("a", "fs-read", "a"), pathCall("b", "fs-read", "b"), pathCall("c", "fs-read", "c"))
	if err != nil {
		t.Fatal(err)
	}
	if got := outcomes(t, v); !sameList(got, []string{"a:ok", "b:ok", "c:ok"}) {
		t.Fatalf("results %v: the order of finishing leaked into the order of the calls", got)
	}
	if got := r.host.ids(log.KindToolCall); !sameList(got, []string{"a", "b", "c"}) {
		t.Fatalf("calls recorded %v", got)
	}
	if got := r.host.ids(log.KindToolResult); !sameList(got, []string{"a", "b", "c"}) {
		t.Fatalf("results recorded %v", got)
	}
	// Each call is recorded before any result, as a reader of the log expects
	// of a call and its answer.
	for i, ev := range r.host.events {
		if ev.kind == log.KindToolResult && i < 3 {
			t.Fatalf("a result was recorded before every call: event %d", i)
		}
	}
}

// Anything that is not a read-only tool, or not only read-only tools, runs in
// order: one call at a time.
func TestBatchesThatAreNotReadOnlyStayInOrder(t *testing.T) {
	for _, tc := range []struct {
		name  string
		calls [][2]string // tool, path
	}{
		{"a read and a write", [][2]string{{"fs-read", "a"}, {"fs-write", "b"}}},
		{"a write and a read", [][2]string{{"fs-write", "a"}, {"fs-read", "b"}}},
		{"two reads of a command", [][2]string{{"shell-read", "a"}, {"shell-read", "b"}}},
		{"two tools of an MCP server that call themselves read-only", [][2]string{{"mcp__docs__search", "a"}, {"mcp__docs__fetch", "b"}}},
		{"two tools the project defines", [][2]string{{"lookup", "a"}, {"lookup", "b"}}},
		{"two commands", [][2]string{{"shell-exec", "a"}, {"shell-exec", "b"}}},
		{"a search that writes its index", [][2]string{{"index-search", "a"}, {"index-search", "b"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newBatchRig(t, 0, "fs-read", "fs-write", "shell-read", "shell-exec", "index-search", "mcp__docs__search", "mcp__docs__fetch", "lookup")
			r.probe.work = func(string, *scheme.Call) error { time.Sleep(20 * time.Millisecond); return nil }
			var calls []llm.ToolCall
			for i, c := range tc.calls {
				calls = append(calls, pathCall(fmt.Sprint(i), c[0], c[1]))
			}
			if _, err := r.run(context.Background(), calls...); err != nil {
				t.Fatal(err)
			}
			if got := r.probe.overlap(); got != 1 {
				t.Fatalf("%d calls ran at once", got)
			}
		})
	}
}

// One call is not a batch, and a call repeated is the progress guard's
// business, which counts runs: both go as they always did.
func TestSingleAndRepeatedCallsStayInOrder(t *testing.T) {
	for _, calls := range [][]llm.ToolCall{
		{pathCall("a", "fs-read", "x")},
		{pathCall("a", "fs-read", "x"), pathCall("b", "fs-read", "x")},
	} {
		r := newBatchRig(t, 0, "fs-read")
		r.probe.work = func(string, *scheme.Call) error { time.Sleep(20 * time.Millisecond); return nil }
		if _, err := r.run(context.Background(), calls...); err != nil {
			t.Fatal(err)
		}
		if got := r.probe.overlap(); got != 1 {
			t.Fatalf("%d calls ran at once", got)
		}
		// In order, each call is followed by its result.
		var want []log.Kind
		for range calls {
			want = append(want, log.KindToolCall, log.KindToolResult)
		}
		var got []log.Kind
		for _, ev := range r.host.events {
			got = append(got, ev.kind)
		}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("events %v, want %v", got, want)
		}
	}
}

func TestConcurrentBatchBudgetStopsPartway(t *testing.T) {
	r := newBatchRig(t, 2, "fs-read")
	bctx, tb, cancel := budget.Start(context.Background(), budget.Limits{ToolCalls: budget.Limit{Soft: 2, Hard: 2}}, nil)
	defer cancel()
	r.l.turn.budget = tb
	v, err := r.run(bctx, pathCall("a", "fs-read", "a"), pathCall("b", "fs-read", "b"), pathCall("c", "fs-read", "c"))
	var halted *scheme.Halt
	if !errors.As(err, &halted) || !strings.Contains(err.Error(), "tool_calls") {
		t.Fatalf("want the budget's stop, got %v (%v)", err, v)
	}
	// The stop cancels the turn, once the calls that fit have run.
	if bctx.Err() == nil {
		t.Fatal("the turn's context was not cancelled by the hard stop")
	}
	// The two reserved calls ran, together, and were answered; the third was
	// refused its slot, so it is answered unrun.
	if got := r.host.ids(log.KindToolCall); !sameList(got, []string{"a", "b"}) {
		t.Fatalf("calls recorded %v", got)
	}
	if got := r.host.ids(log.KindToolResult); !sameList(got, []string{"a", "b", "c"}) {
		t.Fatalf("results recorded %v", got)
	}
	for id, wantOK := range map[string]bool{"a": true, "b": true, "c": false} {
		res := r.host.result(t, id)
		if res.OK != wantOK || (id == "c") != strings.Contains(res.Error, "Not executed") {
			t.Fatalf("result of %s: %+v", id, res)
		}
	}
}

// Calls that do not depend on each other do not stop each other: one failing
// leaves its neighbours to finish.
func TestConcurrentBatchFailureDoesNotStopSiblings(t *testing.T) {
	r := newBatchRig(t, 3, "fs-read")
	r.probe.work = func(path string, _ *scheme.Call) error {
		if path == "b" {
			return errors.New("no such file")
		}
		return nil
	}
	v, err := r.run(context.Background(), pathCall("a", "fs-read", "a"), pathCall("b", "fs-read", "b"), pathCall("c", "fs-read", "c"))
	if err != nil {
		t.Fatal(err)
	}
	if got := outcomes(t, v); !sameList(got, []string{"a:ok", "b:failed", "c:ok"}) {
		t.Fatalf("results %v", got)
	}
}

// The progress guard sees the calls in order and may stop the turn at one of
// them; the calls after it have run, so they are answered as they ended.
func TestConcurrentBatchProgressStopRecordsEveryResult(t *testing.T) {
	r := newBatchRig(t, 3, "fs-read")
	r.probe.work = func(string, *scheme.Call) error { return errors.New("no such file") }
	// Eight failures in a row already: the second failure of the batch is the
	// tenth.
	r.l.turn.progress.errors = 8
	_, err := r.run(context.Background(), pathCall("a", "fs-read", "a"), pathCall("b", "fs-read", "b"), pathCall("c", "fs-read", "c"))
	var stop *ProgressStop
	if !errors.As(err, &stop) || !strings.Contains(stop.Reason, "ten consecutive") {
		t.Fatalf("want the progress stop, got %v", err)
	}
	if got := r.host.ids(log.KindToolResult); !sameList(got, []string{"a", "b", "c"}) {
		t.Fatalf("results recorded %v", got)
	}
	if res := r.host.result(t, "c"); strings.Contains(res.Error, "Not executed") || !strings.Contains(res.Error, "no such file") {
		t.Fatalf("the call after the stop ran, yet is answered %q", res.Error)
	}
	if n := r.host.count(log.KindNote); n != 1 {
		t.Fatalf("%d notes recorded, want the stop's one", n)
	}
}

func TestConcurrentBatchCancel(t *testing.T) {
	r := newBatchRig(t, 2, "fs-read")
	ctx, cancel := context.WithCancelCause(context.Background())
	stopped := errors.New("stopped by the test")
	r.probe.work = func(_ string, c *scheme.Call) error {
		// Both calls are inside: the turn is cancelled under them.
		cancel(stopped)
		<-c.Ctx.Done()
		return c.Ctx.Err()
	}
	_, err := r.run(ctx, pathCall("a", "fs-read", "a"), pathCall("b", "fs-read", "b"))
	if !errors.Is(err, stopped) {
		t.Fatalf("want the cancellation, got %v", err)
	}
	for _, id := range []string{"a", "b"} {
		if res := r.host.result(t, id); res.OK || !strings.Contains(res.Error, "Interrupted") {
			t.Fatalf("result of %s: %+v", id, res)
		}
	}
	if got := r.host.ids(log.KindToolResult); len(got) != 2 {
		t.Fatalf("results recorded %v: a call is left unanswered or answered twice", got)
	}
}

func TestConcurrentBatchIsCapped(t *testing.T) {
	r := newBatchRig(t, 0, "fs-read")
	r.probe.work = func(string, *scheme.Call) error { time.Sleep(30 * time.Millisecond); return nil }
	var calls []llm.ToolCall
	for i := range 3 * maxConcurrentTools {
		calls = append(calls, pathCall(fmt.Sprint(i), "fs-read", fmt.Sprint(i)))
	}
	v, err := r.run(context.Background(), calls...)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(outcomes(t, v)); got != len(calls) {
		t.Fatalf("%d results for %d calls", got, len(calls))
	}
	if got := r.probe.overlap(); got < 2 || got > maxConcurrentTools {
		t.Fatalf("%d calls ran at once, want 2 to %d", got, maxConcurrentTools)
	}
}
