package core

import (
	"bytes"
	"math"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/test/fakes/chat"
)

// A server that says neither how large a model's context is nor what a
// request cost, as OpenAI's and Cerebras' do not, has both from the model's
// own entry (ADR 41, amended 2026-10-05). A conversation on the default
// model, whose [control] only names the entry, is sized for the entry's
// context, and each reply is priced at the entry's prices.
func TestTheDefaultModelIsSizedAndPricedByItsEntry(t *testing.T) {
	t.Parallel()
	srv := chat.New("", chat.Model{ID: "sol"})
	t.Cleanup(srv.Close)
	cfg, err := config.Parse([]byte(`
[servers.openai]
provider = "openai"
dialect = "responses"
url = "` + srv.V1() + `"

[control]
server = "openai"
model = "sol"

[models.openai-sol]
server = "openai"
model = "sol"
num_ctx = 1050000
price_input = 5.0
price_output = 40.0
`))
	if err != nil {
		t.Fatal(err)
	}
	logb := &bytes.Buffer{}
	s, err := New(Options{Config: cfg, Repo: t.TempDir(), NoConfirm: true, LogWriter: logb})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	r := &rig{t: t, s: s, logb: logb}
	if got := r.evalInt("(assoc-ref (model-info) 'num-ctx)"); got != 1050000 {
		t.Errorf("num-ctx %d, want the entry's 1050000", got)
	}
	if got, want := r.evalInt("(assoc-ref (window-limits) 'high-water)"), (1050000-config.Default().Window.OutputReserve)*3/4; got != want {
		t.Errorf("high water %d, want %d, sized for the entry's context", got, want)
	}
	if strings.Contains(logb.String(), "keeps its default size") {
		t.Error("the log says the context length is not known")
	}
	if e := r.turn("hi"); e != "" {
		t.Fatal(e)
	}
	var priced int
	for _, ev := range s.EventsSoFar() {
		if ev.Kind != log.KindLLMResp {
			continue
		}
		var resp log.LLMResp
		if err := ev.Decode(&resp); err != nil {
			t.Fatal(err)
		}
		u := resp.Usage
		want := (float64(u.PromptTokens)*5 + float64(u.OutputTokens)*40) / 1e6
		if u.Cost == nil || !u.CostEstimated || math.Abs(*u.Cost-want) > 1e-12 {
			t.Fatalf("a reply's cost: %v (estimated %v), want %v from the entry's prices", u.Cost, u.CostEstimated, want)
		}
		priced++
	}
	if priced == 0 {
		t.Fatal("no reply was logged")
	}
}
