package rpc

import (
	"encoding/json"
	"testing"
)

// Use only the wire contract so this test also runs on the unchanged daemon,
// where session.totals must fail instead of returning a plausible empty sum.
func TestSessionTotalsReadsOpenAndClosedTasks(t *testing.T) {
	path, _ := start(t, options(t))
	c := connect(t, path)
	c.hello()
	id := opened(t, c, t.TempDir())
	read := func() map[string]any {
		t.Helper()
		var result map[string]any
		if err := json.Unmarshal(c.ok("session.totals", map[string]any{"id": id}), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	first := read()
	if first["started"] != false || first["through"].(float64) < 1 {
		t.Fatalf("empty task: %+v", first)
	}
	subscribed(t, c, id, 0, 100)
	c.ok("session.eval", map[string]any{"id": id, "source": "(+ 1 2)"})
	c.until("eval.result")
	c.until("status")
	next := read()
	if next["started"] != true || next["turns"] != float64(1) || next["last_turn"] == nil || next["through"].(float64) <= first["through"].(float64) {
		t.Fatalf("after eval: %+v", next)
	}
	c.ok("session.close", map[string]any{"id": id})
	closed := read()
	if closed["turns"] != float64(1) || closed["started"] != true {
		t.Fatalf("closed: %+v", closed)
	}
	for _, p := range []map[string]any{{}, {"id": "../secret"}, {"id": "missing"}, {"id": id, "through": 1}} {
		if r := c.call("session.totals", p); r.Error == nil {
			t.Fatalf("accepted invalid parameters: %+v", p)
		}
	}
}
