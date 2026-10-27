package core

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/test/fakes/must"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

// A batch of read-only calls runs concurrently through the real session, its
// guard and its budget, and the model reads the answers in the order of its
// calls (run under -race: the guard, the budget and the log are shared).
func TestReadOnlyBatchThroughTheSession(t *testing.T) {
	t.Parallel()
	r := newRigWith(t, false, func(c *config.Config) { c.Loop.MaxToolCalls = 10 }, fake.Turn{ToolCalls: []fake.ToolCall{
		{ID: "one", Name: "fs-read", Args: map[string]any{"path": "a.txt"}},
		{ID: "two", Name: "fs-read", Args: map[string]any{"path": "b.txt"}},
		{ID: "three", Name: "index-grep", Args: map[string]any{"path": ".", "pattern": "needle"}},
		{ID: "four", Name: "fs-list", Args: map[string]any{"path": "."}},
	}}, fake.Turn{Content: "done"})
	must.WriteFile(t, filepath.Join(r.repo, "a.txt"), []byte("AAA-content\n"), 0o600)
	must.WriteFile(t, filepath.Join(r.repo, "b.txt"), []byte("BBB-content\n"), 0o600)
	must.WriteFile(t, filepath.Join(r.repo, "c.txt"), []byte("needle CCC-content\n"), 0o600)
	if err := r.s.Send("read them"); err != nil {
		t.Fatal(err)
	}
	r.drain("n")
	events, err := log.Read(bytes.NewReader(r.logb.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	var results []string
	calls, resultsBeforeLastCall := 0, 0
	judged := map[string]bool{}
	for _, ev := range events {
		switch ev.Kind {
		case log.KindToolCall:
			calls++
			resultsBeforeLastCall = len(results)
		case log.KindToolResult:
			var res log.ToolResult
			if err := ev.Decode(&res); err != nil {
				t.Fatal(err)
			}
			if !res.OK {
				t.Fatalf("%s failed: %s", res.ID, res.Error)
			}
			results = append(results, res.ID)
		case log.KindGuardDecision:
			var d log.GuardDecision
			if err := ev.Decode(&d); err != nil {
				t.Fatal(err)
			}
			judged[d.Prim] = true
		}
	}
	if want := []string{"one", "two", "three", "four"}; len(results) != 4 || results[0] != want[0] || results[1] != want[1] || results[2] != want[2] || results[3] != want[3] {
		t.Fatalf("results recorded %v, want %v", results, want)
	}
	// The four calls were recorded together, before the first result: in
	// order, each call's result would come before the next call.
	if calls != 4 || resultsBeforeLastCall != 0 {
		t.Fatalf("%d calls recorded, %d results before the last of them: the batch did not run together", calls, resultsBeforeLastCall)
	}
	for _, prim := range []string{"fs-read", "fs-list", "index-grep"} {
		if !judged[prim] {
			t.Fatalf("the guard did not judge %s: %v", prim, judged)
		}
	}
	if len(r.srv.Requests) != 2 {
		t.Fatalf("%d model requests", len(r.srv.Requests))
	}
	request := r.srv.Requests[1].Raw
	a, b, c := bytes.Index(request, []byte("AAA-content")), bytes.Index(request, []byte("BBB-content")), bytes.Index(request, []byte("CCC-content"))
	if a < 0 || a > b || b > c {
		t.Fatalf("the model read the answers out of the order of its calls: %d %d %d", a, b, c)
	}
}
