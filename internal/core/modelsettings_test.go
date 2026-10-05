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

// A server nodwyn does not know, whose model list does not say how much a
// model holds, sizes the window from what providers publish for a model of
// that name (catalogue.Like): 5a76f6e4 and 3e943263 ran gpt-6.1-sol and
// gpt-6-luna, 1M-token models, in 61,440-token windows (ADR 41, amended
// 2026-10-05). A model no provider publishes keeps the default window, and
// the conversation's model state says why, for the console to say.
func TestAnUnknownServerIsSizedByWhatProvidersPublish(t *testing.T) {
	t.Parallel()
	srv := chat.New("", chat.Model{ID: "gpt-6.1-sol"}, chat.Model{ID: "homebrew-7b"})
	t.Cleanup(srv.Close)
	open := func(model string) (*rig, *bytes.Buffer) {
		cfg, err := config.Parse([]byte(`
[control]
provider = "openai"
url = "` + srv.V1() + `"
model = "` + model + `"
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
		return &rig{t: t, s: s, logb: logb}, logb
	}
	known, logb := open("gpt-6.1-sol")
	if got := known.evalInt("(assoc-ref (model-info) 'num-ctx)"); got < 1000000 {
		t.Errorf("num-ctx %d, want what providers publish for gpt-6.1-sol", got)
	}
	if got := known.evalInt("(assoc-ref (window-limits) 'high-water)"); got <= config.Default().Window.HighWater {
		t.Errorf("high water %d: the window kept its default size", got)
	}
	if !strings.Contains(logb.String(), "publishes it for a model of that name") || known.s.Model().ContextUnknown != "" {
		t.Errorf("the note or the model state: %q\n%s", known.s.Model().ContextUnknown, logb.String())
	}
	homebrew, _ := open("homebrew-7b")
	if got := homebrew.evalInt("(assoc-ref (window-limits) 'high-water)"); got != config.Default().Window.HighWater {
		t.Errorf("high water %d for a model nobody publishes", got)
	}
	if why := homebrew.s.Model().ContextUnknown; !strings.Contains(why, "homebrew-7b's context length is not known") {
		t.Errorf("the model state says %q", why)
	}
}
