package core

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/binding"
	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/keychain"
	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/test/fakes/chat"
)

// A server that lists its models only to a key, as OpenAI and Cerebras do,
// is asked for its list with the model's key, after the binding check every
// request with it has. Sessions 20261005T042752Z-871dfe24 (OpenAI, 401) and
// 20261004T182412Z-747045aa (Cerebras, 403) asked without one and kept the
// default window. A key bound for POST alone, as keys saved before were, is
// not sent, and the note says why.
func TestAModelListThatNeedsTheKeyIsAskedWithIt(t *testing.T) {
	t.Parallel()
	const key = "sk-list-key-0123456789"
	row := func(method string) binding.Binding {
		return binding.Binding{Class: "model-key", Dest: "127.0.0.1", Positions: []string{"header:Authorization:Bearer"}, Methods: []string{method}}
	}
	for _, tc := range []struct {
		name   string
		rows   []binding.Binding
		numCtx int
		note   string
	}{
		{"bound for its list", []binding.Binding{row("POST"), row("GET")}, 1050000, "sized for sol's context of 1050000 tokens less its reply cap of 16384"},
		{"bound for POST alone", []binding.Binding{row("POST")}, 0, "method GET at position header:Authorization:Bearer is not bound"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := chat.New(key, chat.Model{ID: "sol", Context: 1050000})
			srv.ListNeedsKey = true
			t.Cleanup(srv.Close)
			cfg, err := config.Parse([]byte(`
[servers.openai]
provider = "openai"
url = "` + srv.V1() + `"
credential = "model-key"

[control]
server = "openai"
model = "sol"
`))
			if err != nil {
				t.Fatal(err)
			}
			kc, err := keychain.Create(filepath.Join(t.TempDir(), "kc"), keychain.FastKDF, []byte("p"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := kc.Add("model-key", []byte(key)); err != nil {
				t.Fatal(err)
			}
			logb := &bytes.Buffer{}
			s, err := New(Options{Config: cfg, Repo: t.TempDir(), NoConfirm: true, LogWriter: logb, Keychain: kc, Bindings: binding.Table{Bind: tc.rows}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { s.Close() })
			r := &rig{t: t, s: s, logb: logb}
			if got := r.evalInt("(assoc-ref (model-info) 'num-ctx)"); got != tc.numCtx {
				t.Errorf("num-ctx %d, want %d", got, tc.numCtx)
			}
			if !strings.Contains(logb.String(), tc.note) {
				t.Errorf("the log does not say %q:\n%.2000s", tc.note, logb.String())
			}
			if strings.Contains(logb.String(), key) {
				t.Fatal("the key reached the log")
			}
			var asked bool
			for _, ev := range s.EventsSoFar() {
				var check log.EgressCheck
				if ev.Kind == log.KindEgressCheck && ev.Decode(&check) == nil && check.Method == "GET" {
					asked = true
					if check.Bound != (tc.numCtx > 0) {
						t.Errorf("the list's egress check: %+v", check)
					}
				}
			}
			if !asked {
				t.Error("no egress check was recorded for the model list")
			}
			// The model's own requests are unchanged either way.
			if e := r.turn("hi"); e != "" {
				t.Fatal(e)
			}
		})
	}
}

// A model whose context is smaller than the default window is sized down to
// fit it, its default reply cap a quarter of the context, and its requests
// carry that cap. The window used to grow only: a 32768-token model kept the
// 61440-token high water and was sent requests it could not hold.
func TestASmallContextShrinksTheWindowAndTheReplyCap(t *testing.T) {
	t.Parallel()
	srv := chat.New("", chat.Model{ID: "small", Context: 32768})
	t.Cleanup(srv.Close)
	cfg, err := config.Parse([]byte(`
[servers.local]
provider = "openai"
url = "` + srv.V1() + `"

[control]
server = "local"
model = "small"
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
	for src, want := range map[string]int{
		"(assoc-ref (window-limits) 'high-water)": (32768 - 8192) * 3 / 4,
		"(assoc-ref (window-limits) 'low-water)":  (32768 - 8192) * 3 / 8,
		"(assoc-ref (window-limits) 'ceiling)":    32768 - 8192,
		"(assoc-ref (model-info) 'max-output)":    8192,
	} {
		if got := r.evalInt(src); got != want {
			t.Errorf("%s = %d, want %d", src, got, want)
		}
	}
	if !strings.Contains(logb.String(), "sized for small's context of 32768 tokens less its reply cap of 8192") {
		t.Errorf("the log does not say how the window was sized:\n%.2000s", logb.String())
	}
	if e := r.turn("hi"); e != "" {
		t.Fatal(e)
	}
	var capped bool
	for _, ev := range s.EventsSoFar() {
		var req log.LLMReq
		if ev.Kind == log.KindLLMReq && ev.Decode(&req) == nil {
			capped = req.MaxOutput == 8192
		}
	}
	if !capped {
		t.Error("the request did not record the 8192-token reply cap, so it would not replay")
	}
	if n := replayAll(t, logb); n != 1 {
		t.Fatalf("replayed %d requests", n)
	}
}
