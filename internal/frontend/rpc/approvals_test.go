package rpc

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/event"
	"github.com/fclaude/nodwyn/internal/keychain"
	"github.com/fclaude/nodwyn/internal/log"
)

func decodePayload[T any](t *testing.T, value any) T {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var result T
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return result
}
func prompt[T any](t *testing.T, c *client, kind string) T {
	t.Helper()
	events := c.until(kind)
	return decodePayload[T](t, events[len(events)-1].Payload)
}
func finishEval(t *testing.T, c *client) log.EvalResult {
	t.Helper()
	result := prompt[log.EvalResult](t, c, string(log.KindEvalResult))
	// Idle is emitted after the execution goroutine releases the busy state.
	c.until(event.Status)
	return result
}
func eval(c *client, id, source string) {
	c.ok("session.eval", map[string]any{"id": id, "source": source})
}
func identity(t *testing.T, c *client) string {
	t.Helper()
	var result helloResult
	if err := json.Unmarshal(c.ok("hello", map[string]any{"protocol": 1}), &result); err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("unix:uid=%d,gid=%d,pid=%d", os.Geteuid(), os.Getegid(), os.Getpid())
	if result.Peer != want {
		t.Fatalf("peer: %q, want %q", result.Peer, want)
	}
	return result.Peer
}

func TestApprovalRepliesUsePeerIdentityAndExplicitScope(t *testing.T) {
	o := options(t)
	path, _ := start(t, o)
	c := connect(t, path)
	if r := c.call("hello", map[string]any{"protocol": 1, "peer": "forged"}); r.Error == nil {
		t.Fatal("accepted forged peer")
	}
	actor := identity(t, c)
	repo := t.TempDir()
	os.MkdirAll(filepath.Join(repo, "notes"), 0700)
	id := opened(t, c, repo)
	subscribed(t, c, id, 0, 100)
	eval(c, id, `(fs-write "first.txt" "one")`)
	p := prompt[event.Confirm](t, c, event.PromptConfirm)
	for _, reply := range []string{"a", "t"} {
		if r := c.call("session.answer", map[string]any{"id": id, "prompt_id": p.ID, "reply": reply}); r.Error == nil {
			t.Fatal("implicit standing approval accepted")
		}
	}
	if r := c.call("session.answer", map[string]any{"id": id, "prompt_id": p.ID, "reply": "y", "actor": "forged"}); r.Error == nil {
		t.Fatal("accepted forged actor")
	}
	c.ok("session.answer", map[string]any{"id": id, "prompt_id": p.ID, "reply": "y"})
	if r := finishEval(t, c); r.Error != "" {
		t.Fatal(r.Error)
	}
	if r := c.call("session.answer", map[string]any{"id": id, "prompt_id": p.ID, "reply": "y"}); r.Error == nil {
		t.Fatal("replayed answer accepted")
	}
	if data, err := os.ReadFile(filepath.Join(repo, "first.txt")); err != nil || string(data) != "one" {
		t.Fatal("write did not run", err)
	}
	found := false
	for _, e := range page(t, c, id, 0, 0, 1000).Events {
		if e.Kind == log.KindGuardDecision {
			var d log.GuardDecision
			json.Unmarshal(e.Payload, &d)
			if d.PromptID == p.ID {
				found = true
				if d.Actor != actor || d.Scope != "once" || d.Answer != "yes" {
					t.Fatalf("receipt: %+v", d)
				}
			}
		}
	}
	if !found {
		t.Fatal("missing attributed decision")
	}
	eval(c, id, `(fs-write "notes/second.txt" "two")`)
	p = prompt[event.Confirm](t, c, event.PromptConfirm)
	c.ok("session.approval.create", map[string]any{"id": id, "prompt_id": p.ID, "scope": "session"})
	if r := finishEval(t, c); r.Error != "" {
		t.Fatal(r.Error)
	}
	var info sessionDetails
	json.Unmarshal(c.ok("session.info", map[string]any{"id": id}), &info)
	if len(info.Writes) != 1 {
		t.Fatalf("standing writes: %+v", info.Writes)
	}
	eval(c, id, `(fs-write "notes/third.txt" "three")`)
	// If standing approval is not installed, this blocks at a prompt and fails.
	if r := finishEval(t, c); r.Error != "" {
		t.Fatal(r.Error)
	}
	found = false
	for _, e := range page(t, c, id, 0, 0, 1000).Events {
		if e.Kind == log.KindApprovalChange {
			var a log.ApprovalChange
			json.Unmarshal(e.Payload, &a)
			found = true
			if a.Actor != actor || a.PromptID != p.ID || a.Scope != "session" || a.Kind != "write" {
				t.Fatalf("grant: %+v", a)
			}
		}
	}
	if !found {
		t.Fatal("missing grant audit")
	}
	// Reopening the log does not reinstate authority from audit records.
	c.ok("session.close", map[string]any{"id": id})
	if n := c.notice(); n.Method != "session.closed" {
		t.Fatal("missing close notification")
	}
	c.ok("session.resume", map[string]any{"id": id})
	info = sessionDetails{}
	json.Unmarshal(c.ok("session.info", map[string]any{"id": id}), &info)
	if len(info.Writes) != 0 {
		t.Fatal("resume restored transient authority")
	}
	subscribed(t, c, id, 0, 100)
	eval(c, id, `(fs-write "first.txt" "resumed")`)
	resumed := prompt[event.Confirm](t, c, event.PromptConfirm)
	if resumed.ID == p.ID {
		t.Fatal("prompt ID reused after resume")
	}
	if r := c.call("session.answer", map[string]any{"id": id, "prompt_id": p.ID, "reply": "y"}); r.Error == nil {
		t.Fatal("old lifetime's prompt accepted")
	}
	c.ok("session.answer", map[string]any{"id": id, "prompt_id": resumed.ID, "reply": "n"})
	if r := finishEval(t, c); r.Error == "" {
		t.Fatal("denied resumed write ran")
	}

}

func TestSecurityAndProtectedPromptsCannotCreateStandingApproval(t *testing.T) {
	for _, tc := range []struct {
		name, source string
		security     bool
	}{
		// Protected paths hold code that later runs as the user, so their
		// writes are security prompts too (guard.ProtectedPaths).
		{"protected", `(fs-write ".nodwyn/allow.toml" "test")`, true},
		{"security", `(fs-write "secret.txt" "sk-ant-abcdefgh1234")`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, _ := start(t, options(t))
			c := connect(t, path)
			c.hello()
			repo := t.TempDir()
			os.MkdirAll(filepath.Join(repo, ".nodwyn"), 0700)
			id := opened(t, c, repo)
			subscribed(t, c, id, 0, 100)
			eval(c, id, tc.source)
			p := prompt[event.Confirm](t, c, event.PromptConfirm)
			if p.Prompt.Security != tc.security {
				t.Fatalf("security flag: %+v", p.Prompt)
			}
			for _, scope := range []string{"turn", "session"} {
				if r := c.call("session.approval.create", map[string]any{"id": id, "prompt_id": p.ID, "scope": scope}); r.Error == nil {
					t.Fatal("widened protected/security approval")
				}
			}
			c.ok("session.answer", map[string]any{"id": id, "prompt_id": p.ID, "reply": "n"})
			if r := finishEval(t, c); r.Error == "" {
				t.Fatal("denial ignored")
			}
		})
	}
}

func TestSecretReplyStaysOutsideSchemeAndHistory(t *testing.T) {
	const canary = "rpc-private-canary-5ca84e7f"
	o := options(t)
	store, err := keychain.Create(filepath.Join(t.TempDir(), "keychain"), keychain.FastKDF, []byte("test-passphrase"))
	if err != nil {
		t.Fatal(err)
	}
	o.Keychain = store
	path, _ := start(t, o)
	c := connect(t, path)
	actor := identity(t, c)
	id := opened(t, c, t.TempDir())
	subscribed(t, c, id, 0, 100)
	eval(c, id, `(define credential (credential-register "test-key"))`)
	p := prompt[event.SecretPrompt](t, c, event.PromptSecret)
	if p.Class != "test-key" {
		t.Fatal("wrong class")
	}
	for _, params := range []map[string]any{
		{"id": id, "prompt_id": p.ID}, {"id": id, "prompt_id": p.ID, "secret": nil},
	} {
		if r := c.call("session.secret", params); r.Error == nil {
			t.Fatal("missing/null secret consumed prompt")
		}
	}
	if r := c.call("session.send", map[string]any{"id": id, "text": "overlap"}); r.Error == nil {
		t.Fatal("eval was not serialized")
	}
	raw := c.ok("session.secret", map[string]any{"id": id, "prompt_id": p.ID, "secret": canary})
	if strings.Contains(string(raw), canary) {
		t.Fatal("secret echoed")
	}
	if r := finishEval(t, c); r.Error != "" {
		t.Fatal(r.Error)
	}
	if store.Count() != 1 {
		t.Fatal("credential was not registered")
	}
	h := store.HandleForClass("test-key")
	req, _ := http.NewRequest("GET", "https://example.invalid", nil)
	if err := store.InjectHeader(h.ID, "Authorization", "Bearer ", req); err != nil {
		t.Fatal(err)
	}
	if req.Header.Get("Authorization") != "Bearer "+canary {
		t.Fatal("stored credential changed")
	}
	eval(c, id, `credential`)
	result := finishEval(t, c)
	if result.Error != "" || strings.Contains(result.Value, canary) || !strings.Contains(result.Value, "#<cred:test-key:") {
		t.Fatal("Scheme did not receive an opaque handle")
	}
	events := page(t, c, id, 0, 0, 1000).Events
	found := false
	for _, e := range events {
		if strings.Contains(string(e.Payload), canary) {
			t.Fatal("secret entered durable log")
		}
		if e.Kind == log.KindCredRegister {
			var r log.CredRegister
			json.Unmarshal(e.Payload, &r)
			found = true
			if r.Actor != actor || r.PromptID != p.ID || r.Handle != h.ID {
				t.Fatalf("registration receipt: %+v", r)
			}
		}
	}
	if !found {
		t.Fatal("missing registration audit")
	}
	observed, _ := json.Marshal(c.observed)
	if strings.Contains(string(observed), canary) {
		t.Fatal("secret entered live events or RPC responses")
	}
	disk, err := os.ReadFile(log.SessionPath(o.Paths.SessionsDir, id))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(disk), canary) {
		t.Fatal("secret entered disk log")
	}
	eval(c, id, `(credential-register "canceled-key")`)
	pending := prompt[event.SecretPrompt](t, c, event.PromptSecret)
	c.ok("session.abort", map[string]any{"id": id})
	if r := finishEval(t, c); r.Error == "" {
		t.Fatal("canceled registration succeeded")
	}
	if r := c.call("session.secret", map[string]any{"id": id, "prompt_id": pending.ID, "secret": canary}); r.Error == nil {
		t.Fatal("stale secret accepted")
	}
	if store.Count() != 1 {
		t.Fatal("canceled registration persisted")
	}
	eval(c, id, `(+ 1 2)`)
	if r := finishEval(t, c); r.Value != "3" || r.Error != "" {
		t.Fatalf("eval after cancellation: %+v", r)
	}
}

func TestBudgetReplyIsAttributedWithoutToolAuthority(t *testing.T) {
	o := options(t)
	o.Config.Budget.ActionsSoft = 1
	o.Config.Budget.ActionsHard = 3
	path, _ := start(t, o)
	c := connect(t, path)
	actor := identity(t, c)
	repo := t.TempDir()
	os.MkdirAll(filepath.Join(repo, "notes"), 0700)
	id := opened(t, c, repo)
	subscribed(t, c, id, 0, 100)
	eval(c, id, `(begin (fs-write "notes/a" "a") (fs-write "notes/b" "b"))`)
	p := prompt[event.Confirm](t, c, event.PromptConfirm)
	c.ok("session.approval.create", map[string]any{"id": id, "prompt_id": p.ID, "scope": "turn"})
	cp := prompt[event.Checkpoint](t, c, event.PromptCheckpoint)
	if r := c.call("session.approval.create", map[string]any{"id": id, "prompt_id": cp.ID, "scope": "session"}); r.Error == nil {
		t.Fatal("checkpoint granted tool authority")
	}
	c.ok("session.answer", map[string]any{"id": id, "prompt_id": cp.ID, "reply": "3"})
	if r := finishEval(t, c); r.Error != "" {
		t.Fatal(r.Error)
	}
	found := false
	for _, e := range page(t, c, id, 0, 0, 1000).Events {
		if e.Kind == log.KindTurnCheckpoint {
			var record log.TurnCheckpoint
			json.Unmarshal(e.Payload, &record)
			found = true
			if record.Actor != actor || record.PromptID != cp.ID || record.Choice.Soft != 3 {
				t.Fatalf("checkpoint receipt: %+v", record)
			}
		}
	}
	if !found {
		t.Fatal("checkpoint was not recorded")
	}
	var info sessionDetails
	json.Unmarshal(c.ok("session.info", map[string]any{"id": id}), &info)
	if info.TurnWrites || len(info.Writes) > 0 || len(info.Commands) > 0 {
		t.Fatal("authority survived turn/checkpoint")
	}
}

func TestEvalCancellationAndRedaction(t *testing.T) {
	path, _ := start(t, options(t))
	c := connect(t, path)
	c.hello()
	id := opened(t, c, t.TempDir())
	subscribed(t, c, id, 0, 100)
	eval(c, id, `(let loop () (loop))`)
	c.until(string(log.KindEvalReq))
	c.ok("session.abort", map[string]any{"id": id})
	if r := finishEval(t, c); r.Error == "" {
		t.Fatal("canceled eval succeeded")
	}
	eval(c, id, `"sk-ant-abcdefgh1234"`)
	if r := finishEval(t, c); r.Error != "" || strings.Contains(r.Value, "sk-ant-") {
		t.Fatal("eval result was not redacted")
	}
	for _, e := range page(t, c, id, 0, 0, 1000).Events {
		if strings.Contains(string(e.Payload), "sk-ant-abcdefgh1234") {
			t.Fatal("eval source/result persisted unredacted")
		}
		if e.Kind == log.KindUserMsg || e.Kind == log.KindLLMReq {
			t.Fatal("eval entered the model turn path")
		}
	}
}

func TestPeerIdentityFromDifferentProcess(t *testing.T) {
	if path := os.Getenv("NODWYN_TEST_PEER_SOCKET"); path != "" {
		c := connect(t, path)
		identity(t, c) // Must name this child, not the daemon process.
		return
	}
	path, _ := start(t, options(t))
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, "-test.run=^TestPeerIdentityFromDifferentProcess$")
	// Without the race detector's one-second sleep as it exits.
	cmd.Env = append(os.Environ(), "NODWYN_TEST_PEER_SOCKET="+path, "GORACE="+strings.TrimSpace(os.Getenv("GORACE")+" atexit_sleep_ms=0"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("peer process: %v\n%s", err, out)
	}
}

// "Don't ask" is turned on from the computer nodwyn runs on: session.info
// says so and who did it, and a write goes through unasked. A paired
// device may turn it off, never on.
func TestApproveAllOverTheProtocol(t *testing.T) {
	o := options(t)
	o.Config.Approval.DontAsk = true
	path, _ := start(t, o)
	c := connect(t, path)
	me := identity(t, c)
	repo := t.TempDir()
	id := opened(t, c, repo)
	subscribed(t, c, id, 0, 100)
	c.ok("session.approve_all", map[string]any{"id": id, "on": true})
	var info sessionDetails
	json.Unmarshal(c.ok("session.info", map[string]any{"id": id}), &info)
	if !info.ApproveAll || info.ApproveActor != me {
		t.Fatalf("info: %v %q", info.ApproveAll, info.ApproveActor)
	}
	eval(c, id, `(fs-write "a.txt" "a")`)
	if result := finishEval(t, c); result.Error != "" {
		t.Fatal(result.Error)
	}
	if b, err := os.ReadFile(filepath.Join(repo, "a.txt")); err != nil || string(b) != "a" {
		t.Fatalf("a.txt: %q %v", b, err)
	}
	phone := connect(t, path)
	phone.ok("hello", map[string]any{"protocol": 1, "via": "web:phone"})
	if r := phone.call("session.approve_all", map[string]any{"id": id, "on": true}); r.Error == nil || !strings.Contains(r.Error.Message, "not from a paired device") {
		t.Fatalf("a paired device turned it on: %+v", r.Error)
	}
	phone.ok("session.approve_all", map[string]any{"id": id, "on": false})
	json.Unmarshal(c.ok("session.info", map[string]any{"id": id}), &info)
	if info.ApproveAll {
		t.Fatal("still on after the device turned it off")
	}
	if r := c.call("session.approve_all", map[string]any{"id": id}); r.Error == nil {
		t.Fatal("accepted without on")
	}
}

// "Don't ask" is offered only once Settings offers it (approval.dont_ask,
// off by default): until then session.approve_all refuses to turn it on and
// session.info says it is not offered. Settings turning the offer on lets a
// task switch it on; turning it off again switches it off in the open task
// as well, and a write there asks once more.
func TestApproveAllNeedsTheSettingAndLosesItWithIt(t *testing.T) {
	o := options(t)
	if o.Config.Approval.DontAsk {
		t.Fatal("approval.dont_ask is on by default")
	}
	path, _ := start(t, o)
	c := connect(t, path)
	me := identity(t, c)
	id := opened(t, c, t.TempDir())
	subscribed(t, c, id, 0, 100)
	var info sessionDetails
	json.Unmarshal(c.ok("session.info", map[string]any{"id": id}), &info)
	if info.ApproveAllOffered {
		t.Fatal("offered with the setting off")
	}
	if r := c.call("session.approve_all", map[string]any{"id": id, "on": true}); r.Error == nil || !strings.Contains(r.Error.Message, "Settings") {
		t.Fatalf("turned on with the setting off: %+v", r.Error)
	}
	json.Unmarshal(c.ok("session.info", map[string]any{"id": id}), &info)
	if info.ApproveAll {
		t.Fatal("on after the refusal")
	}
	c.ok("settings.write", map[string]any{"set": map[string]any{"approval.dont_ask": true}})
	json.Unmarshal(c.ok("session.info", map[string]any{"id": id}), &info)
	if !info.ApproveAllOffered {
		t.Fatal("not offered once Settings offers it")
	}
	c.ok("session.approve_all", map[string]any{"id": id, "on": true})
	json.Unmarshal(c.ok("session.info", map[string]any{"id": id}), &info)
	if !info.ApproveAll || info.ApproveActor != me {
		t.Fatalf("info: %v %q", info.ApproveAll, info.ApproveActor)
	}
	c.ok("settings.write", map[string]any{"set": map[string]any{"approval.dont_ask": false}})
	json.Unmarshal(c.ok("session.info", map[string]any{"id": id}), &info)
	if info.ApproveAll || info.ApproveAllOffered {
		t.Fatalf("still on (%v) or offered (%v) after Settings took it away", info.ApproveAll, info.ApproveAllOffered)
	}
	raw, err := os.ReadFile(o.Paths.ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "dont_ask = false") {
		t.Fatalf("config.toml:\n%s", raw)
	}
	if r := c.call("settings.write", map[string]any{"set": map[string]any{"approval.dont_ask": "yes"}}); r.Error == nil {
		t.Fatal("approval.dont_ask took a string")
	}
}
