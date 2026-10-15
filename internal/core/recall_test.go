package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/llm"
	"github.com/fclaude/nodwyn/internal/log"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

// The model searches its own conversation and finds a result it saw
// earlier, redacted as it was recorded, without finding its own search;
// recall brings the result back whole, in ranges, and a reply with the
// seq of its call's result (ADR 50).
func TestModelSearchesAndRecallsItsConversation(t *testing.T) {
	t.Parallel()
	const canary = "sk-ant-abcdefgh1234"
	r := newRig(t, true,
		fake.Turn{ToolCalls: []fake.ToolCall{{ID: "read", Name: "fs-read", Args: map[string]any{"path": "note.txt"}}}},
		fake.Turn{ToolCalls: []fake.ToolCall{{ID: "find", Name: "index-search", Args: map[string]any{"query": "frobnicator 7731", "scope": "conversation"}}}},
		fake.Turn{Content: "done"},
	)
	if err := os.WriteFile(filepath.Join(r.repo, "note.txt"), []byte("the frobnicator failed with code 7731\nkey "+canary+"\nnothing else\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := r.s.Send("why did it fail?"); err != nil {
		t.Fatal(err)
	}
	r.drain("n")
	if len(r.srv.Requests) != 3 {
		t.Fatalf("%d requests", len(r.srv.Requests))
	}
	var readSeq, replySeq uint64
	events, err := log.Read(bytes.NewReader(r.logb.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range events {
		var p struct {
			ID        string         `json:"id"`
			ToolCalls []log.ToolCall `json:"tool_calls"`
		}
		_ = json.Unmarshal(ev.Payload, &p)
		switch {
		case ev.Kind == log.KindToolResult && p.ID == "read":
			readSeq = ev.Seq
		case ev.Kind == log.KindLLMResp && len(p.ToolCalls) == 1 && p.ToolCalls[0].ID == "read":
			replySeq = ev.Seq
		}
	}
	if readSeq == 0 || replySeq == 0 {
		t.Fatalf("no read (%d) or its reply (%d) in the log", readSeq, replySeq)
	}

	// What the model was shown of its search.
	var shown string
	for _, raw := range r.srv.Requests[2].Messages {
		var m struct{ Role, Content string }
		if json.Unmarshal(raw, &m) == nil && m.Role == "tool" && strings.Contains(m.Content, "best of") {
			shown = m.Content
		}
	}
	heading := fmt.Sprintf(`seq %d, the result of fs-read {"path":"note.txt"}`, readSeq)
	if !strings.Contains(shown, heading) || !strings.Contains(shown, "  1: the frobnicator failed with code 7731") {
		t.Fatalf("the search did not show the read: %q", shown)
	}
	if strings.Contains(shown, "index-search") || strings.Contains(shown, "nothing else") {
		t.Fatalf("the search found itself, or showed a line without the words: %q", shown)
	}
	if bytes.Contains(r.srv.Requests[2].Raw, []byte(canary)) || bytes.Contains(r.logb.Bytes(), []byte(canary)) {
		t.Fatal("the credential reached the model or the log")
	}

	eval := func(src string) string {
		t.Helper()
		v, err := r.s.Eval(t.Context(), src)
		if err != nil {
			t.Fatalf("%s: %v", src, err)
		}
		return v.S
	}
	whole := eval(fmt.Sprintf("(recall %d)", readSeq))
	want := "[" + heading + "]\n[lines 1-3 of 3]\nthe frobnicator failed with code 7731\nkey [REDACTED"
	if !strings.HasPrefix(whole, want) || strings.Contains(whole, canary) {
		t.Fatalf("recall: %q", whole)
	}
	if got := eval(fmt.Sprintf("(recall %d 3 1)", readSeq)); got != "["+heading+"]\n[lines 3-3 of 3]\nnothing else\n" {
		t.Fatalf("a range: %q", got)
	}
	reply := eval(fmt.Sprintf("(recall %d)", replySeq))
	if !strings.Contains(reply, `fs-read {"path":"note.txt"}`) || !strings.Contains(reply, fmt.Sprintf("[the result of fs-read is seq %d]", readSeq)) {
		t.Fatalf("a reply and where its result is: %q", reply)
	}
	taint, err := r.s.Eval(t.Context(), fmt.Sprintf("(taint-of (recall %d))", readSeq))
	if err != nil || !strings.Contains(taint.String(), "external:log") {
		t.Fatalf("recall's provenance: %v, %v", taint, err)
	}
	if got := eval(`(index-search "zzqqxx" "conversation")`); !strings.HasPrefix(got, "No event of this conversation holds") {
		t.Fatalf("no match: %q", got)
	}
	for src, msg := range map[string]string{
		`(index-search "frobnicator" "earlier")`:               "not built yet",
		`(index-search "frobnicator" "conversation" 8 "docs")`: "not of the conversation",
		`(index-search "! ?" "conversation")`:                  "no words",
		`(recall 1)`:                                           "is not a message, a reply or a tool result",
		`(recall 999999)`:                                      "there is no seq 999999",
		fmt.Sprintf("(recall %d 9 1)", readSeq):                "past the end",
	} {
		if _, err := r.s.Eval(t.Context(), src); err == nil || !strings.Contains(err.Error(), msg) {
			t.Errorf("%s: %v, want an error saying %q", src, err, msg)
		}
	}
}

// A resumed task holds only the end of its log (ADR 49 §10); its
// conversation is searched and recalled whole, the start read from disk,
// and a sub-agent's work is named as its.
func TestRecallReadsAResumedTasksWholeLog(t *testing.T) {
	t.Parallel()
	o := diskOptions(t)
	o.Provider = waitingProvider{}
	id := "20260901T070000Z-0000dddd"
	longLog(t, o, id, 400, 395, func(write func(log.Kind, any)) {
		write(log.KindUserMsg, log.UserMsg{Text: "plan the fix"})
		write(log.KindContextNarrow, log.ContextNarrow{System: "explore", Agent: "explorer"})
		write(log.KindLLMResp, log.LLMResp{ToolCalls: []log.ToolCall{{ID: "e1", Name: "fs-read", Args: json.RawMessage(`{"path":"gizmo.go"}`)}}})
		write(log.KindToolResult, log.ToolResult{ID: "e1", Name: "fs-read", OK: true, Value: "func Gizmo() {}"})
		write(log.KindContextFold, log.ContextFold{})
		write(log.KindLLMResp, log.LLMResp{ToolCalls: []log.ToolCall{
			{ID: "s1", Name: "shell-exec", Args: json.RawMessage(`{"cmd":"make","args":["check"]}`)},
			{ID: "g1", Name: "index-grep", Args: json.RawMessage(`{"path":".","pattern":"Gizmo"}`)},
		}})
		write(log.KindToolResult, log.ToolResult{ID: "s1", Name: "shell-exec", OK: true, Value: `{"status":2,"stdout":"building\nlinking","stderr":"undefined: Gizmo"}`})
		write(log.KindToolResult, log.ToolResult{ID: "g1", Name: "index-grep", OK: true, Value: `{"matches":[{"path":"gizmo.go","line":1,"text":"func Gizmo() {}"}],"truncated":false}`})
		write(log.KindTurnEnd, log.TurnEnd{Outcome: "completed"})
	})
	o.Repo = ""
	s, err := Resume(o, id)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	held := s.EventsSoFar()
	// The first turn's result: session.start, the limits, then run 0's
	// message, reply and result.
	const first = 5
	if held[0].Seq <= first {
		t.Fatalf("the resumed session holds seq %d on; the test needs it to start later", held[0].Seq)
	}
	got, err := s.Eval(t.Context(), fmt.Sprintf("(recall %d)", first))
	if err != nil || got.S != fmt.Sprintf("[seq %d, the result of fs-list {\"path\":\".\"}]\n[lines 1-1 of 1]\nlisting 0", first) {
		t.Fatalf("recall of the log's start: %q, %v", got.S, err)
	}
	found, err := s.Eval(t.Context(), `(index-search "listing" "conversation" 50)`)
	if err != nil {
		t.Fatal(err)
	}
	var searched int
	if _, err := fmt.Sscanf(found.S, "[the 20 best of %d events", &searched); err != nil || searched < 1600 {
		t.Fatalf("a search of the whole conversation, at most 20 found: %q (%v)", found.S[:min(200, len(found.S))], err)
	}
	gizmo, err := s.Eval(t.Context(), `(index-search "Gizmo" "conversation")`)
	if err != nil || !strings.Contains(gizmo.S, `the result of fs-read {"path":"gizmo.go"}, in the explorer's context`) {
		t.Fatalf("a sub-agent's result: %q, %v", gizmo.S, err)
	}
	// A shell's result and a search's, one line of JSON each in the log,
	// come back with lines: the shell's as the model read it, whole.
	var shellSeq, grepSeq uint64
	whole, err := log.ReadFile(log.SessionPath(o.Paths.SessionsDir, id))
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range whole {
		var p log.ToolResult
		if ev.Kind == log.KindToolResult && ev.Decode(&p) == nil {
			switch p.ID {
			case "s1":
				shellSeq = ev.Seq
			case "g1":
				grepSeq = ev.Seq
			}
		}
	}
	shell, err := s.Eval(t.Context(), fmt.Sprintf("(recall %d 2 2)", shellSeq))
	if err != nil || !strings.HasSuffix(shell.S, "[lines 2-3 of 5]\nbuilding\nlinking\n") {
		t.Fatalf("a shell result's lines: %q, %v", shell.S, err)
	}
	if all, err := s.Eval(t.Context(), fmt.Sprintf("(recall %d)", shellSeq)); err != nil || !strings.HasSuffix(all.S, "exit 2\nbuilding\nlinking\n[stderr]\nundefined: Gizmo") {
		t.Fatalf("a shell result: %q, %v", all.S, err)
	}
	grep, err := s.Eval(t.Context(), fmt.Sprintf("(recall %d)", grepSeq))
	if err != nil || !strings.Contains(grep.S, "\n      \"path\": \"gizmo.go\",\n") {
		t.Fatalf("a JSON result, indented: %q, %v", grep.S, err)
	}
	if strings.Contains(found.S, "in the compaction's context") {
		t.Fatalf("a compaction is not an agent's context: %q", found.S)
	}
}

// The model searches the repository: the index lives with the project's
// notes, every file it read has its guard decision and the build a summary
// in the log, and the model reads the result redacted (ADR 50).
func TestModelSearchesTheRepository(t *testing.T) {
	t.Parallel()
	const canary = "sk-ant-abcdefgh1234"
	srv := fake.NewT(t,
		fake.Turn{ToolCalls: []fake.ToolCall{{ID: "s", Name: "index-search", Args: map[string]any{"query": "binding checker host", "scope": "repo"}}}},
		fake.Turn{Content: "done"},
	)
	o := diskOptions(t)
	o.Provider = llm.NewOllama(srv.URL)
	if err := os.WriteFile(filepath.Join(o.Repo, "egress.go"), []byte("package egress\n\n// checkBinding refuses a key the binding does not name for the host: "+canary+"\nfunc checkBinding() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Send("where is the binding checked?"); err != nil {
		t.Fatal(err)
	}
	(&rig{t: t, s: s}).drain("n")
	request := srv.Requests[1].Raw
	if !bytes.Contains(request, []byte("egress.go:1-4: package egress")) || !bytes.Contains(request, []byte("[REDACTED")) || bytes.Contains(request, []byte(canary)) {
		t.Fatalf("the model's view of the search: %s", request)
	}
	raw, err := os.ReadFile(log.SessionPath(o.Paths.SessionsDir, s.ID()))
	if err != nil {
		t.Fatal(err)
	}
	events, err := log.Read(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	var built *log.IndexUpdate
	reads := 0
	for _, ev := range events {
		switch ev.Kind {
		case log.KindIndexUpdate:
			var u log.IndexUpdate
			if ev.Decode(&u) == nil {
				built = &u
			}
		case log.KindGuardDecision:
			if bytes.Contains(ev.Payload, []byte(`"prim":"fs-read"`)) && bytes.Contains(ev.Payload, []byte("egress.go")) {
				reads++
			}
		}
	}
	if built == nil || built.Read < 1 || built.Root != config.CanonicalPath(o.Repo) {
		t.Fatalf("the build's summary: %+v", built)
	}
	if reads < 2 {
		t.Fatalf("%d guarded reads of egress.go: the build's and the result's", reads)
	}
	if bytes.Contains(raw, []byte(canary)) {
		t.Fatal("the credential reached the log")
	}
	trees, _ := filepath.Glob(filepath.Join(projectDir(o.Paths.DataDir, s.project()), "index", "trees", "*.json"))
	if len(trees) != 1 {
		t.Fatalf("the index is not with the project's notes: %v", trees)
	}
}
