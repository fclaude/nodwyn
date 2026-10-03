package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/event"
	"github.com/fclaude/nodwyn/internal/llm"
	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/window"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

func diskOptions(t *testing.T) Options {
	t.Helper()
	return Options{Repo: t.TempDir(), Paths: config.PathsUnder(t.TempDir(), t.TempDir()), Config: config.Default(), NoConfirm: true}
}

func TestResumeConversation(t *testing.T) {
	srv := fake.NewT(t, fake.Turn{Content: "remembered answer"}, fake.Turn{Content: "continued answer"})
	defer srv.Close()
	o := diskOptions(t)
	o.Provider = llm.NewOllama(srv.URL)
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Send("remember my project"); err != nil {
		t.Fatal(err)
	}
	(&rig{t: t, s: s}).drain("n")
	id := s.ID()
	s.Close()
	before, _ := os.ReadFile(log.SessionPath(o.Paths.SessionsDir, id))
	o.Repo = ""
	resumed, err := Resume(o, id)
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close()
	if resumed.ID() != id || resumed.Busy() {
		t.Fatal("resume must preserve identity and stay idle")
	}
	if err := resumed.Send("continue"); err != nil {
		t.Fatal(err)
	}
	(&rig{t: t, s: resumed}).drain("n")
	raw := string(srv.Requests[1].Raw)
	if !strings.Contains(raw, "remember my project") || !strings.Contains(raw, "remembered answer") {
		t.Fatal("history missing from resumed context")
	}
	after, _ := os.ReadFile(log.SessionPath(o.Paths.SessionsDir, id))
	if !strings.HasPrefix(string(after), string(before)) {
		t.Fatal("resume rewrote history")
	}
	infos, err := (Store{Dir: o.Paths.SessionsDir}).List()
	if err != nil || len(infos) != 1 || infos[0].FirstMessage != "remember my project" || infos[0].InFlight {
		t.Fatalf("store: %+v, %v", infos, err)
	}
}

func TestResumeMissingResultsAndApprovals(t *testing.T) {
	o := diskOptions(t)
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	id := s.ID()
	s.allow.Add("dangerous command")
	s.writes.Add(o.Repo)
	s.turnTrust = true
	if _, err := s.Eval(context.Background(), "(define before-restart 42)"); err != nil {
		t.Fatal(err)
	}
	_, err = s.Record(log.KindLLMResp, log.LLMResp{ToolCalls: []log.ToolCall{
		{ID: "done", Name: "fs-read", Args: json.RawMessage(`{}`)},
		{ID: "missing", Name: "fs-write", Args: json.RawMessage(`{}`)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	s.Record(log.KindToolResult, log.ToolResult{ID: "done", Name: "fs-read", OK: true, Value: "kept"})
	s.Close()
	f, err := os.OpenFile(log.SessionPath(o.Paths.SessionsDir, id), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(`{"seq":`)
	f.Close()
	resumed, err := Resume(o, id)
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close()
	if _, err := resumed.Eval(context.Background(), "before-restart"); err == nil {
		t.Fatal("Scheme definition survived restart")
	}
	if len(resumed.Allowed()) != 0 || len(resumed.AllowedWrites()) != 0 || resumed.turnTrust {
		t.Fatal("session approvals survived")
	}
	results := 0
	for _, ev := range resumed.EventsSoFar() {
		if ev.Kind == log.KindToolResult {
			results++
			var result log.ToolResult
			ev.Decode(&result)
			if result.ID == "missing" && (result.OK || !strings.Contains(result.Error, "Outcome unknown")) {
				t.Fatalf("bad interrupted result: %+v", result)
			}
		}
	}
	if results != 2 {
		t.Fatalf("results: %d", results)
	}
	if _, err := Resume(o, id); err == nil {
		t.Fatal("concurrent resume accepted")
	}
}

func TestResumeRejectsWrongRepoAndUnsafeIDs(t *testing.T) {
	o := diskOptions(t)
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	id := s.ID()
	s.Close()
	o.Repo = t.TempDir()
	if _, err := Resume(o, id); err == nil {
		t.Fatal("wrong repository accepted")
	}
	o.Repo = ""
	for _, bad := range []string{"../escape", "", "a/b"} {
		if _, err := Resume(o, bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
	resumed, err := Resume(o, id)
	if err != nil {
		t.Fatal("failed resume leaked lock:", err)
	}
	resumed.Close()
}

// This helper is killed while waiting on a model request, without Close.
// crashPaths puts nodwyn's configuration and data in folders of their own
// under the crash test's repository: the guard denies the session's own
// directories, so the repository itself cannot be one of them.
func crashPaths(dir string) config.Paths {
	return config.PathsUnder(filepath.Join(dir, ".config"), filepath.Join(dir, ".data"))
}

func TestResumeCrashHelper(t *testing.T) {
	dir := os.Getenv("NODWYN_TEST_CRASH_DIR")
	if dir == "" {
		return
	}
	o := Options{Repo: dir, Paths: crashPaths(dir), Config: config.Default(), Provider: llm.NewOllama(os.Getenv("NODWYN_TEST_CRASH_URL"))}
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "session-id"), []byte(s.ID()), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.Send("write the marker once"); err != nil {
		t.Fatal(err)
	}
	for ev := range s.Events() {
		if ev.Kind == event.PromptConfirm {
			if err := s.Answer(ev.Payload.(event.Confirm).ID, "y"); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestResumeAfterProcessKilled(t *testing.T) {
	dir := t.TempDir()
	waiting := make(chan struct{})
	release := make(chan struct{})
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		calls++
		if calls == 1 {
			fmt.Fprintln(w, `{"message":{"role":"assistant","tool_calls":[{"id":"write-once","function":{"name":"fs-write","arguments":{"path":"marker","content":"written"}}}]},"done":true}`)
			return
		}
		close(waiting)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer srv.Close()
	defer close(release)
	child := exec.Command(os.Args[0], "-test.run=^TestResumeCrashHelper$")
	child.Env = append(os.Environ(), "NODWYN_TEST_CRASH_DIR="+dir, "NODWYN_TEST_CRASH_URL="+srv.URL)
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if child.ProcessState == nil {
			child.Process.Kill()
			child.Wait()
		}
	}()
	select {
	case <-waiting:
	case <-time.After(15 * time.Second):
		t.Fatal("child did not complete the write")
	}
	child.Process.Kill()
	child.Wait()
	marker, err := os.ReadFile(filepath.Join(dir, "marker"))
	if err != nil || string(marker) != "written" {
		t.Fatalf("write: %q %v", marker, err)
	}
	id, _ := os.ReadFile(filepath.Join(dir, "session-id"))
	next := fake.NewT(t, fake.Turn{Content: "the marker was already written"})
	defer next.Close()
	o := Options{Paths: crashPaths(dir), Config: config.Default(), Provider: llm.NewOllama(next.URL), NoConfirm: true}
	info, err := (Store{Dir: o.Paths.SessionsDir}).Info(string(id))
	if err != nil || !info.InFlight {
		t.Fatalf("interruption: %+v %v", info, err)
	}
	s, err := Resume(o, string(id))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Send("continue without repeating the write"); err != nil {
		t.Fatal(err)
	}
	(&rig{t: t, s: s}).drain("n")
	if !strings.Contains(string(next.Requests[0].Raw), "write-once") {
		t.Fatal("completed tool missing from context")
	}
	events, err := log.ReadFile(log.SessionPath(o.Paths.SessionsDir, string(id)))
	if err != nil {
		t.Fatal(err)
	}
	writes := 0
	for _, ev := range events {
		if ev.Kind == log.KindToolCall {
			writes++
		}
	}
	if writes != 1 {
		t.Fatalf("write replayed: %d calls", writes)
	}
}

func TestUnfinishedToolsWithReusedIDs(t *testing.T) {
	var data bytes.Buffer
	writer := log.New(&data, "reused")
	writer.Append(log.KindLLMResp, log.LLMResp{ToolCalls: []log.ToolCall{{ID: "call_0", Name: "fs-write", Args: json.RawMessage(`{"path":"interrupted"}`)}}})
	writer.Append(log.KindLLMResp, log.LLMResp{ToolCalls: []log.ToolCall{{ID: "call_0", Name: "fs-write", Args: json.RawMessage(`{"path":"completed"}`)}}})
	writer.Append(log.KindToolResult, log.ToolResult{ID: "call_0", Name: "fs-write", OK: true, Value: "written"})
	events, err := log.Read(bytes.NewReader(data.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	pending, err := unfinishedTools(events)
	if err != nil || len(pending) != 1 || !strings.Contains(string(pending[0].Args), "interrupted") {
		t.Fatalf("pending: %+v %v", pending, err)
	}
	// A repair result must discharge the older call so another resume does not
	// add the same recovery record again.
	writer.Append(log.KindToolResult, log.ToolResult{ID: "call_0", Name: "fs-write", Error: "Outcome unknown"})
	events, err = log.Read(bytes.NewReader(data.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	pending, err = unfinishedTools(events)
	if err != nil || len(pending) != 0 {
		t.Fatalf("already repaired: %+v %v", pending, err)
	}
}

// Resuming is a full reload: .nodwyn/init.scm runs again, so the prompts and
// tools it defines now apply, and a request made before the resume still
// replays under the recipe it was made with.
func TestResumeRereadsInit(t *testing.T) {
	srv := fake.NewT(t, fake.Turn{Content: "first"}, fake.Turn{Content: "second"})
	defer srv.Close()
	o := diskOptions(t)
	o.Provider = llm.NewOllama(srv.URL)
	writeInit := func(version string) {
		os.MkdirAll(filepath.Join(o.Repo, ".nodwyn"), 0o755)
		os.WriteFile(filepath.Join(o.Repo, ".nodwyn", "init.scm"), []byte(`
(define (system-prompt repo) "PROMPT `+version+`")
(register-tool "tool-`+version+`" (lambda () "ok") '((doc . "a tool") (params . ())))`), 0o644)
	}
	writeInit("one")
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Send("hi"); err != nil {
		t.Fatal(err)
	}
	(&rig{t: t, s: s}).drain("n")
	id := s.ID()
	s.Close()

	writeInit("two")
	o.Repo = ""
	resumed, err := Resume(o, id)
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close()
	var names []string
	for _, tl := range resumed.Tools() {
		names = append(names, tl.Name)
	}
	if got := strings.Join(names, " "); !strings.Contains(got, "tool-two") || strings.Contains(got, "tool-one") {
		t.Fatalf("resumed tools: %s", got)
	}
	if err := resumed.Send("again"); err != nil {
		t.Fatal(err)
	}
	(&rig{t: t, s: resumed}).drain("n")
	if first, second := string(srv.Requests[0].Raw), string(srv.Requests[1].Raw); !strings.Contains(first, "PROMPT one") ||
		!strings.Contains(second, "PROMPT two") || strings.Contains(second, "PROMPT one") {
		t.Fatalf("system prompts: before %q, after %q", first[:120], second[:120])
	}
	events := resumed.EventsSoFar()
	replayed := 0
	for _, ev := range events {
		if ev.Kind == log.KindLLMReq {
			if _, err := window.ReplayRequest(events, ev.Seq); err != nil {
				t.Fatalf("replay of seq %d: %v", ev.Seq, err)
			}
			replayed++
		}
	}
	if replayed != 2 {
		t.Fatalf("replayed %d requests", replayed)
	}
}
