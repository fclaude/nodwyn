package core

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/event"
	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/window"
)

// reasoningArray is a reply's reasoning_details as OpenRouter sends them: a
// summary and an encrypted blob of about size bytes of base64, n's own.
func reasoningArray(n, size int) string {
	sum := sha256.Sum256([]byte(fmt.Sprint("reasoning ", n)))
	var b []byte
	for len(b)*4/3 < size {
		b = append(b, sum[:]...)
		sum = sha256.Sum256(sum[:])
	}
	return fmt.Sprintf(`[{"type":"reasoning.summary","summary":"Step %d: list the files.","id":"rs_%d","format":"unknown","index":0},`+
		`{"type":"reasoning.encrypted","data":"%s","id":"rs_%d","format":"unknown","index":1}]`, n, n, base64.StdEncoding.EncodeToString(b), n)
}

// reasonedCall and reasonedText are assistant replies that carry
// reasoning_details beside their thinking, "thought n", as OpenRouter's do;
// reasonedCall lists the repository, or its .nodwyn every other time, so no
// call is the one before it again.
func reasonedCall(n int, details string) string {
	path := []string{".", ".nodwyn"}[n%2]
	return fmt.Sprintf(`{"role":"assistant","content":null,"reasoning":"thought %d","reasoning_details":%s,"tool_calls":[{"id":"c%d","type":"function","function":{"name":"fs-list","arguments":"{\"path\":\"%s\"}"}}]}`, n, details, n, path)
}
func reasonedText(n int, text, details string) string {
	return fmt.Sprintf(`{"role":"assistant","content":%s,"reasoning":"thought %d","reasoning_details":%s}`, quote(text), n, details)
}

// sentReasoning is, for each assistant message of a request body in order, the
// reasoning_details it carried as sent, "" for none, and its thinking.
func sentReasoning(t *testing.T, body string) (details, thinking []string) {
	t.Helper()
	var req struct {
		Messages []map[string]json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatal(err)
	}
	for _, m := range req.Messages {
		if string(m["role"]) != `"assistant"` {
			if m["reasoning_details"] != nil {
				t.Errorf("a %s message carries reasoning_details", m["role"])
			}
			continue
		}
		details = append(details, string(m["reasoning_details"]))
		var text string
		_ = json.Unmarshal(m["reasoning_content"], &text)
		thinking = append(thinking, text)
	}
	return details, thinking
}

// untilAnswered asks model until a reply calls no tools, running the calls.
func untilAnswered(model string) string {
	ask := "(model-turn)"
	if model != "" {
		ask = fmt.Sprintf("(model-turn %q)", model)
	}
	return `
(define (agent-turn request)
  (let loop ((reply ` + ask + `))
    (unless (null? (assoc-ref reply 'tool-calls '()))
      (run-tools (assoc-ref reply 'tool-calls))
      (loop ` + ask + `))))`
}

// tomlSession is a session whose config.toml is toml, running init.
func tomlSession(t *testing.T, toml, init string) (*rig, *bytes.Buffer) {
	t.Helper()
	cfg, err := config.Parse([]byte(toml))
	if err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".nodwyn"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".nodwyn", "init.scm"), []byte(init), 0o644); err != nil {
		t.Fatal(err)
	}
	logb := &bytes.Buffer{}
	s, err := newInitSession(Options{Config: cfg, Repo: repo, LogWriter: logb, NoConfirm: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return &rig{t: t, s: s, logb: logb}, logb
}

// A chat-completions reply's reasoning_details go back on it, byte for byte
// and in order, in every later request of the conversation, the next turn's
// too, over three rounds of calls; the reply's thinking is shown and the
// details are not. The log keeps them, so every request replays to its hash
// with each reply's details as they came, and they are in that hash: a
// request before any reply carried them is the request it was (ADR 9,
// amended 2026-10-15).
func TestReasoningDetailsGoBackWithTheirReplyAndReplay(t *testing.T) {
	t.Parallel()
	d := []string{reasoningArray(1, 4000), reasoningArray(2, 4000), reasoningArray(3, 4000)}
	impl := newFakeOpenAI(t, reasonedCall(1, d[0]), reasonedCall(2, d[1]), reasonedCall(3, d[2]), oaiText("listed"), oaiText("again"))
	r := modelRig(t, untilAnswered("impl"), map[string]config.Model{"impl": {Provider: "openai", URL: impl.URL + "/v1", Model: "m"}}, Options{})
	if err := r.s.Send("list files"); err != nil {
		t.Fatal(err)
	}
	for _, ev := range r.drain("n") {
		switch ev.Kind {
		case event.Error:
			t.Fatal(ev.Payload.(event.ErrorInfo).Err)
		case event.StreamText, event.StreamThinking:
			if text := ev.Payload.(event.Text).Text; strings.Contains(text, "reasoning.encrypted") || strings.Contains(text, d[0][120:160]) {
				t.Errorf("the reasoning_details were shown: %q", text)
			}
		}
	}
	if e := r.turn("again"); e != "" {
		t.Fatal(e)
	}
	want := [][]string{nil, {d[0]}, {d[0], d[1]}, {d[0], d[1], d[2]}, {d[0], d[1], d[2], ""}}
	seen := impl.seen()
	if len(seen) != len(want) {
		t.Fatalf("%d requests", len(seen))
	}
	for i, s := range seen {
		details, thinking := sentReasoning(t, s.body)
		if !slices.Equal(details, want[i]) {
			t.Errorf("request %d carried reasoning_details %.80q, want %.80q", i, details, want[i])
		}
		for k, text := range thinking {
			if k < len(d) && text != "" {
				t.Errorf("request %d: reply %d went with its thinking %q beside its reasoning_details", i, k, text)
			}
		}
	}

	if n := replayAll(t, r.logb); n != len(want) {
		t.Fatalf("replayed %d requests", n)
	}
	evs, err := log.Read(bytes.NewReader(r.logb.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	var requests []log.Event
	for _, ev := range evs {
		if ev.Kind == log.KindLLMReq {
			requests = append(requests, ev)
		}
	}
	first, err := window.ReplayRequest(evs, requests[0].Seq)
	if err != nil {
		t.Fatal(err)
	}
	if raw, _ := json.Marshal(first); bytes.Contains(raw, []byte(`"blocks"`)) {
		t.Errorf("the request before any reasoning_details carries blocks: %s", raw)
	}
	last, err := window.ReplayRequest(evs, requests[len(requests)-1].Seq)
	if err != nil {
		t.Fatal(err)
	}
	var rebuilt []string
	for _, m := range last.Messages {
		if m.Role != "assistant" {
			continue
		}
		data := ""
		for _, b := range m.Blocks {
			if b.Type == "reasoning_details" && b.Origin != "" {
				data += b.Data
			}
		}
		rebuilt = append(rebuilt, data)
	}
	if !slices.Equal(rebuilt, want[len(want)-1]) {
		t.Errorf("replay rebuilt reasoning_details %.80q", rebuilt)
	}
	var rec log.LLMReq
	if err := requests[len(requests)-1].Decode(&rec); err != nil {
		t.Fatal(err)
	}
	for i := range last.Messages {
		last.Messages[i].Blocks = nil
	}
	if stripped, _ := window.RequestHash(last); stripped == rec.RequestHash {
		t.Error("the request's hash does not cover its reasoning_details")
	}
}

// Trimming takes a reply's reasoning_details with it. A reply made before the
// window changed what came before it loses its own too, as a Claude reply's
// signed thinking does, and goes back with its thinking text instead; a reply
// made after keeps them. Every request replays.
func TestTrimmingTakesAReplysReasoningDetails(t *testing.T) {
	t.Parallel()
	big := reasoningArray(1, 90000)
	d := []string{big, reasoningArray(2, 300), reasoningArray(3, 300), reasoningArray(4, 300)}
	impl := newFakeOpenAI(t, reasonedCall(1, d[0]), reasonedCall(2, d[1]), reasonedCall(3, d[2]), reasonedCall(4, d[3]), oaiText("listed"))
	r, logb := tomlSession(t, fmt.Sprintf(`[control]
provider = "openai"
url = %q
model = "m"
num_ctx = 65536
[window]
high_water = 30000
low_water = 20000
max_result_tokens = 1000
output_reserve = 4096
`, impl.URL+"/v1"), untilAnswered(""))
	if e := r.turn("list files"); e != "" {
		t.Fatal(e)
	}
	want := []struct {
		details, thinking []string
	}{
		{nil, nil},
		{[]string{d[0]}, []string{""}},
		{[]string{d[0], d[1]}, []string{"", ""}},
		{[]string{"", "", ""}, []string{"", "thought 2", "thought 3"}},
		{[]string{"", "", "", d[3]}, []string{"", "thought 2", "thought 3", ""}},
	}
	seen := impl.seen()
	if len(seen) != len(want) {
		t.Fatalf("%d requests", len(seen))
	}
	for i, s := range seen {
		details, thinking := sentReasoning(t, s.body)
		if !slices.Equal(details, want[i].details) || !slices.Equal(thinking, want[i].thinking) {
			t.Errorf("request %d carried reasoning_details %.60q and thinking %q, want %.60q and %q", i, details, thinking, want[i].details, want[i].thinking)
		}
	}
	var elided []int
	for _, ev := range r.s.EventsSoFar() {
		if ev.Kind == log.KindLLMReq {
			var rec log.LLMReq
			if err := ev.Decode(&rec); err != nil {
				t.Fatal(err)
			}
			elided = append(elided, len(rec.Elided))
		}
	}
	if !slices.Equal(elided, []int{0, 0, 0, 2, 2}) {
		t.Fatalf("the requests elided %v entries; want the first reply and its result from the fourth on", elided)
	}
	if n := replayAll(t, logb); n != len(want) {
		t.Fatalf("replayed %d requests", n)
	}
}

// A conversation moved to another model sends it none of the reasoning_details
// the first one gave, neither to another server nor to another model on the
// same one, and their thinking goes as before; moved back, the first model
// gets its own again, where they were, and not the other model's (ADR 40).
func TestReasoningDetailsStayWithTheModelThatGaveThem(t *testing.T) {
	t.Parallel()
	d := []string{reasoningArray(1, 2000), reasoningArray(2, 2000), reasoningArray(3, 2000)}
	home := newFakeOpenAI(t, reasonedCall(1, d[0]), reasonedText(2, "listed", d[1]), reasonedText(3, "sibling", d[2]), oaiText("back"))
	away := newFakeOpenAI(t, oaiText("away"))
	r, logb := tomlSession(t, fmt.Sprintf(`[control]
provider = "openai"
url = %[1]q
model = "m"
num_ctx = 200000
[models.away]
provider = "openai"
url = %[2]q
model = "m"
num_ctx = 200000
[models.sibling]
provider = "openai"
url = %[1]q
model = "m2"
num_ctx = 200000
`, home.URL+"/v1", away.URL+"/v1"), untilAnswered(""))
	if e := r.turn("list files"); e != "" {
		t.Fatal(e)
	}
	for _, name := range []string{"away", "sibling", "control"} {
		if _, err := r.s.SetModel(ModelChoice{Name: name}); err != nil {
			t.Fatal(err)
		}
		if e := r.turn("and now?"); e != "" {
			t.Fatal(e)
		}
	}
	h, a := home.seen(), away.seen()
	if len(h) != 4 || len(a) != 1 {
		t.Fatalf("home got %d requests, away %d", len(h), len(a))
	}
	for _, tc := range []struct {
		name, body        string
		details, thinking []string
	}{
		{"away", a[0].body, []string{"", ""}, []string{"thought 1", "thought 2"}},
		{"sibling", h[2].body, []string{"", "", ""}, []string{"thought 1", "thought 2", ""}},
		{"back", h[3].body, []string{d[0], d[1], "", ""}, []string{"", "", "", "thought 3"}},
	} {
		details, thinking := sentReasoning(t, tc.body)
		if !slices.Equal(details, tc.details) || !slices.Equal(thinking, tc.thinking) {
			t.Errorf("%s: reasoning_details %.60q and thinking %q, want %.60q and %q", tc.name, details, thinking, tc.details, tc.thinking)
		}
	}
	if n := replayAll(t, logb); n != 5 {
		t.Fatalf("replayed %d requests", n)
	}
}

// reasoning_details that hold text the log redacts are not kept: what the log
// keeps would not be what the provider made. The reply goes back with its
// thinking, as before, the log says why, and the next reply's go back.
func TestReasoningDetailsTheLogWouldChangeAreNotKept(t *testing.T) {
	t.Parallel()
	const key = "sk-proj-Q2xhdWRlRmFrZUtleUZvclRlc3RzMDEyMzQ1"
	secret := `[{"type":"reasoning.summary","summary":"The fixture's key is ` + key + `.","format":"unknown","index":0}]`
	clean := reasoningArray(2, 2000)
	impl := newFakeOpenAI(t, reasonedCall(1, secret), reasonedCall(2, clean), oaiText("listed"))
	r := modelRig(t, untilAnswered("impl"), map[string]config.Model{"impl": {Provider: "openai", URL: impl.URL + "/v1", Model: "m"}}, Options{})
	if e := r.turn("list files"); e != "" {
		t.Fatal(e)
	}
	seen := impl.seen()
	if len(seen) != 3 {
		t.Fatalf("%d requests", len(seen))
	}
	details, thinking := sentReasoning(t, seen[2].body)
	if !slices.Equal(details, []string{"", clean}) || !slices.Equal(thinking, []string{"thought 1", ""}) {
		t.Errorf("reasoning_details %.60q, thinking %q", details, thinking)
	}
	if strings.Contains(r.logb.String(), key) {
		t.Error("the key reached the log")
	}
	if !strings.Contains(r.logb.String(), "reasoning_details held text the log redacts") {
		t.Error("the log does not say why the reply's reasoning_details were not kept")
	}
	if n := replayAll(t, r.logb); n != 3 {
		t.Fatalf("replayed %d requests", n)
	}
}

// A narrowed context's replies carry their reasoning_details while it lasts,
// and folding it takes them away with it.
func TestFoldingAContextTakesItsReasoningDetails(t *testing.T) {
	t.Parallel()
	d := reasoningArray(1, 2000)
	impl := newFakeOpenAI(t, reasonedCall(1, d), oaiText("side done"), oaiText("main"))
	r := modelRig(t, `
(define (agent-turn request)
  (narrow-context "You list files.")
  (add-message "side work")
  (let loop ((reply (model-turn "impl")))
    (unless (null? (assoc-ref reply 'tool-calls '()))
      (run-tools (assoc-ref reply 'tool-calls))
      (loop (model-turn "impl"))))
  (fold-context)
  (model-turn "impl"))`, map[string]config.Model{"impl": {Provider: "openai", URL: impl.URL + "/v1", Model: "m"}}, Options{})
	if e := r.turn("list files"); e != "" {
		t.Fatal(e)
	}
	seen := impl.seen()
	if len(seen) != 3 {
		t.Fatalf("%d requests", len(seen))
	}
	if details, _ := sentReasoning(t, seen[1].body); !slices.Equal(details, []string{d}) {
		t.Errorf("inside the narrowed context: %.60q", details)
	}
	if details, _ := sentReasoning(t, seen[2].body); len(details) != 0 || strings.Contains(seen[2].body, "reasoning_details") {
		t.Errorf("after the fold: %.60q", details)
	}
	if n := replayAll(t, r.logb); n != 3 {
		t.Fatalf("replayed %d requests", n)
	}
}
