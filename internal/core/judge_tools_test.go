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

// The judge's primitives, judge-yes-no, judge-score and judge-choice, are
// registered when the session starts with the judge's server's key stored,
// and offered to the model only at judge.content = "full"; below that they
// are Scheme's alone (ADR 46).
func TestJudgeToolsOfferedAtFullContentWithAKey(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		content    string
		key        bool
		registered bool
		want       bool
	}{
		{config.JudgeContentFull, true, true, true},
		{config.JudgeContentPlan, true, true, false},
		{config.JudgeContentFull, false, false, false},
	} {
		srv := fake.NewT(t)
		t.Cleanup(srv.Close)
		cfg := config.Default()
		cfg.Servers = map[string]config.Server{"jev": {Provider: "jev", URL: "https://api.typesafe.ai", Credential: "jev-key"}}
		cfg.Judge.Server, cfg.Judge.Content = "jev", tc.content
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
		got := strings.Join(names, " ")
		for _, name := range []string{"judge-yes-no", "judge-score", "judge-choice"} {
			if _, ok := s.interp.Lookup(name); ok != tc.registered {
				t.Fatalf("%+v: %s registered = %v", tc, name, ok)
			}
			if strings.Contains(" "+got+" ", " "+name+" ") != tc.want {
				t.Fatalf("%+v: %s offered = %v; tools: %s", tc, name, !tc.want, got)
			}
		}
		s.Close()
	}
}

// What the judge used is recorded with each answer, priced at [judge]'s
// prices (the judge reports what it used, never what it cost), and counted
// in the task's spend; its tokens stay out of the token budget, which counts
// model tokens. What a provider publishes is the price when [judge] sets
// none (TestJudgeOptionsTakePublishedPrices).
func TestJudgeUsageIsRecordedAndPriced(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		setup func(o *Options)
	}{
		{"at [judge]'s prices", func(o *Options) {
			in, out := 1000.0, 2000.0
			o.Config.Judge.PriceInput, o.Config.Judge.PriceOutput = &in, &out
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			judge := newFakeJudge(t, noul(0.2), noul(0.75))
			r := judgedRigWith(t, judge, config.JudgeContentFull, "", tc.setup,
				fake.Turn{ToolCalls: []fake.ToolCall{{Name: "judge-yes-no", Args: map[string]any{"state": "the diff", "instructions": "is it complete?"}}}},
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
		})
	}
}

// A model's judge-yes-no call goes through the session: the guard checks
// jev-key's binding, the key is injected, the answer comes back as the
// tool's result, and jev.req says the model sent it.
func TestModelCallsTheJudge(t *testing.T) {
	t.Parallel()
	judge := newFakeJudge(t, noul(0.2), noul(0.75))
	r := judgedRigAt(t, judge, config.JudgeContentFull, "",
		fake.Turn{ToolCalls: []fake.ToolCall{{Name: "judge-yes-no", Args: map[string]any{"state": "the diff", "instructions": "is it complete?"}}}},
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
	if len(reqs) != 2 || reqs[0].Tool != "judge-yes-no" || reqs[0].Origin != "harness" || reqs[1].Tool != "judge-yes-no" || reqs[1].Origin != "model" || !reqs[1].OK {
		t.Fatalf("jev.req records: %+v", reqs)
	}
	if result != "0.75" {
		t.Fatalf("tool result %q", result)
	}
	if strings.Contains(r.logb.String(), "sk-judge-canary-0001") {
		t.Fatal("the jev-key value reached the log")
	}
}
