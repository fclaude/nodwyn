package core

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/llm"
	"github.com/fclaude/nodwyn/internal/log"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

// The request's wall time reaches llm.resp: it is what a session's tok/s
// adds up, and the one timing a provider that reports none of its own
// still gets. The fields are read by name from the payload, not through
// log.Usage, so the test shows what a log holds.
func TestModelWallTimeReachesTheLog(t *testing.T) {
	t.Parallel()
	// A slow fake: the wall time must survive its truncation to whole
	// milliseconds, and stay the request's own, not the 1500 ms the fake
	// reports for itself.
	srv := fake.NewFunc(func(fake.Request) fake.Turn {
		time.Sleep(30 * time.Millisecond)
		return fake.Turn{Content: "done"}
	})
	defer srv.Close()
	cfg := config.Default()
	cfg.Control = config.Model{Provider: "ollama", URL: srv.URL, Model: "m"}
	logb := &bytes.Buffer{}
	s, err := New(Options{Config: cfg, Repo: t.TempDir(), Provider: llm.NewOllama(srv.URL), NoConfirm: true, LogWriter: logb})
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{t: t, s: s, logb: logb}
	begun := time.Now()
	if e := r.turn("hello"); e != "" {
		t.Fatal(e)
	}
	turn := time.Since(begun).Milliseconds()
	s.Close()
	replies := 0
	for _, ev := range r.logged() {
		if ev.Kind != log.KindLLMResp {
			continue
		}
		replies++
		var p struct {
			Usage struct {
				WallMS       int64 `json:"wall_ms"`
				OutputTokens int   `json:"output_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatal(err)
		}
		if p.Usage.OutputTokens != 44 {
			t.Fatalf("output tokens %d, want the fake's 44", p.Usage.OutputTokens)
		}
		// The request is inside the turn, so no load makes it longer than
		// the turn; a fixed ceiling would be a guess about the machine.
		// A retry's earlier attempts staying out of it is
		// llm.TestChatRetryingTimesTheSuccessfulAttempt's.
		if p.Usage.WallMS < 30 || p.Usage.WallMS > turn {
			t.Fatalf("wall_ms %d, want the request's own: at least the fake's 30 ms and at most the turn's %d ms", p.Usage.WallMS, turn)
		}
	}
	if replies == 0 {
		t.Fatal("no model reply in the log")
	}
}
