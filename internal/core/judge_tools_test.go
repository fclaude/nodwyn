package core

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/keychain"
	"github.com/fclaude/nodwyn/internal/llm"
	"github.com/fclaude/nodwyn/internal/log"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

// The model is offered judge-noul, judge-score and judge-choice only when
// the session starts with a jev-key stored and jev.content = "full"; the
// handle-taking jev-* primitives are never offered.
func TestJudgeToolsOfferedAtFullContentWithAKey(t *testing.T) {
	for _, tc := range []struct {
		content string
		key     bool
		want    bool
	}{
		{config.JevContentFull, true, true},
		{config.JevContentPlan, true, false},
		{config.JevContentFull, false, false},
	} {
		srv := fake.New()
		t.Cleanup(srv.Close)
		cfg := config.Default()
		cfg.Jev.Content = tc.content
		kc, err := keychain.Create(filepath.Join(t.TempDir(), "kc"), keychain.FastKDF, []byte("p"))
		if err != nil {
			t.Fatal(err)
		}
		if tc.key {
			kc.Add("jev-key", []byte("sk-judge-canary-0001"))
		}
		s, err := New(Options{Config: cfg, Repo: t.TempDir(), Provider: llm.NewOllama(srv.URL), NoConfirm: true, LogWriter: &bytes.Buffer{}, Keychain: kc})
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, tool := range s.Tools() {
			names = append(names, tool.Name)
		}
		s.Close()
		got := strings.Join(names, " ")
		if strings.Contains(got, "jev-") {
			t.Fatalf("%+v: a handle-taking jev primitive is offered: %s", tc, got)
		}
		for _, name := range []string{"judge-noul", "judge-score", "judge-choice"} {
			if strings.Contains(" "+got+" ", " "+name+" ") != tc.want {
				t.Fatalf("%+v: %s offered = %v; tools: %s", tc, name, !tc.want, got)
			}
		}
	}
}

// What the judge used is recorded with each answer, priced when [jev] has
// prices, and counted in the task's spend; its tokens stay out of the
// token budget, which counts model tokens.
func TestJudgeUsageIsRecordedAndPriced(t *testing.T) {
	judge := newFakeJudge(t, noul(0.2), noul(0.75))
	r := judgedRigWith(t, judge, config.JevContentFull, "", func(o *Options) {
		in, out := 1000.0, 2000.0
		o.Config.Jev.PriceInput, o.Config.Jev.PriceOutput = &in, &out
	},
		fake.Turn{ToolCalls: []fake.ToolCall{{Name: "judge-noul", Args: map[string]any{"state": "the diff", "instructions": "is it complete?"}}}},
		fake.Turn{Content: "done"})
	if e := r.turn("check it"); e != "" {
		t.Fatal(e)
	}
	var total float64
	n := 0
	for _, ev := range r.logged() {
		if ev.Kind != log.KindJevReq {
			continue
		}
		var jr log.JevReq
		ev.Decode(&jr)
		n++
		u := jr.Usage
		if jr.Model != "jev-1.13.0" || u == nil || u.InputTokens <= 0 || u.OutputTokens != 20 || u.Cost == nil {
			t.Fatalf("jev.req %+v usage %+v", jr, u)
		}
		if want := (float64(u.InputTokens)*1000 + 20*2000) / 1e6; *u.Cost != want {
			t.Fatalf("cost %v, want %v", *u.Cost, want)
		}
		total += *u.Cost
	}
	if n != 2 {
		t.Fatalf("%d judge requests", n)
	}
	if got := r.s.knownSpend(); got != total {
		t.Fatalf("the task's spend is %v, the judge's %v (the model has no prices)", got, total)
	}
}

// A model's judge-noul call goes through the session: the guard checks
// jev-key's binding, the key is injected, the answer comes back as the
// tool's result, and jev.req says the model sent it.
func TestModelCallsTheJudge(t *testing.T) {
	judge := newFakeJudge(t, noul(0.2), noul(0.75))
	r := judgedRigAt(t, judge, config.JevContentFull, "",
		fake.Turn{ToolCalls: []fake.ToolCall{{Name: "judge-noul", Args: map[string]any{"state": "the diff", "instructions": "is it complete?"}}}},
		fake.Turn{Content: "done"})
	if e := r.turn("check it"); e != "" {
		t.Fatal(e)
	}
	if strings.Join(judge.asked(), " ") != "noul noul" {
		t.Fatalf("judge asked %v", judge.asked())
	}
	evs, err := log.Read(bytes.NewReader(r.logb.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	var reqs []log.JevReq
	var result string
	for _, ev := range evs {
		switch ev.Kind {
		case log.KindJevReq:
			var jr log.JevReq
			ev.Decode(&jr)
			reqs = append(reqs, jr)
		case log.KindToolResult:
			var tr log.ToolResult
			ev.Decode(&tr)
			result = tr.Value + tr.Error
		}
	}
	if len(reqs) != 2 || reqs[0].Tool != "jev-noul" || reqs[0].Origin != "harness" || reqs[1].Tool != "judge-noul" || reqs[1].Origin != "model" || !reqs[1].OK {
		t.Fatalf("jev.req records: %+v", reqs)
	}
	if result != "0.75" {
		t.Fatalf("tool result %q", result)
	}
	if strings.Contains(r.logb.String(), "sk-judge-canary-0001") {
		t.Fatal("the jev-key value reached the log")
	}
}
