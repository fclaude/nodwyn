package window

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/fclaude/nibb/internal/llm"
	"github.com/fclaude/nibb/internal/log"
)

func TestReplayKeepsHistoricalWindowAcrossLaterTurns(t *testing.T) {
	s := newSession()
	recipe := log.WindowContext{Version: RecipeVersion, System: "sys", Tools: json.RawMessage(`[]`), HighWater: pol.HighWater, LowWater: pol.LowWater, MaxResultTokens: pol.MaxResultTokens, NumCtx: 9000, KeepAlive: "5m"}
	s.w.Append(log.KindSessionStart, log.SessionStart{Context: &recipe, Model: "test"})
	expected := map[uint64][]byte{}
	trimmed := false
	for range 6 {
		s.turn(3000)
		built := Build(s.events(t), "sys", Estimate("[]"), pol)
		trimmed = trimmed || built.Trimmed
		req := llm.Request{Model: "test", Messages: built.Messages, Tools: []llm.Tool{}, NumCtx: 9000, KeepAlive: "5m"}
		digest, err := RequestHash(req)
		if err != nil {
			t.Fatal(err)
		}
		ev, err := s.w.Append(log.KindLLMReq, log.LLMReq{Model: "test", Elided: built.Elided, RequestHash: digest})
		if err != nil {
			t.Fatal(err)
		}
		expected[ev.Seq], _ = json.Marshal(req)
	}
	if !trimmed {
		t.Fatal("test did not exercise elision")
	}
	events := s.events(t)
	before, _ := json.Marshal(events)
	for seq, want := range expected {
		got, err := ReplayRequest(events, seq)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(got)
		if !bytes.Equal(raw, want) {
			t.Fatalf("request %d changed with later history", seq)
		}
	}
	after, _ := json.Marshal(events)
	if !bytes.Equal(before, after) {
		t.Fatal("replay mutated history")
	}
}

func TestReplayRejectsUnverifiableRequests(t *testing.T) {
	for _, mode := range []string{"legacy", "unknown recipe", "changed history", "resume without recipe", "missing", "wrong kind"} {
		t.Run(mode, func(t *testing.T) {
			s := newSession()
			recipe := log.WindowContext{Version: RecipeVersion, System: "sys", Tools: json.RawMessage(`[]`), HighWater: 3000, LowWater: 1500, MaxResultTokens: 100}
			if mode == "unknown recipe" {
				recipe.Version = "future"
			}
			context := &recipe
			if mode == "legacy" {
				context = nil
			}
			s.w.Append(log.KindSessionStart, log.SessionStart{Context: context})
			s.w.Append(log.KindUserMsg, log.UserMsg{Text: "original"})
			built := Build(s.events(t), "sys", Estimate("[]"), Policy{HighWater: 3000, LowWater: 1500, MaxResultTokens: 100})
			hash, err := RequestHash(llm.Request{Messages: built.Messages, Tools: []llm.Tool{}})
			if err != nil {
				t.Fatal(err)
			}
			if mode == "resume without recipe" {
				s.w.Append(log.KindSessionResume, log.SessionStart{})
			}
			ev, err := s.w.Append(log.KindLLMReq, log.LLMReq{RequestHash: hash})
			if err != nil {
				t.Fatal(err)
			}
			events := s.events(t)
			seq := ev.Seq
			if mode == "changed history" {
				events[1].Payload = json.RawMessage(`{"text":"changed"}`)
			}
			if mode == "missing" {
				seq = 999
			}
			if mode == "wrong kind" {
				seq = 2
			}
			if _, err := ReplayRequest(events, seq); err == nil {
				t.Fatal("unverifiable request accepted")
			}
		})
	}
}

func TestReplayDoesNotExposeSecretsFromUnverifiedHistory(t *testing.T) {
	events := []log.Event{{Seq: 1, Kind: log.KindNote}, {Seq: 2, Kind: log.KindUserMsg, Payload: json.RawMessage(`{"text":"sk-ant-abcdefgh1234"}`)}, {Seq: 3, Kind: log.KindLLMReq, Payload: json.RawMessage(`{}`)}}
	got, err := ReplayRequest(events, 3)
	raw, _ := json.Marshal(got)
	if err == nil || strings.Contains(string(raw), "sk-ant-") {
		t.Fatal("unsafe unverifiable output")
	}
}
