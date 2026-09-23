package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fclaude/nibb/internal/event"
	"github.com/fclaude/nibb/internal/frontend/rpc"
	"github.com/fclaude/nibb/internal/keychain"
)

// Exercise prompt replies over the actual authenticated WebSocket, with the
// existing terminal RPC producing operations unavailable in the browser.
func checkBrowserPrompts(t *testing.T, terminal *rpc.Client, ctx context.Context, id, repo string, store *keychain.Store,
	call func(string, any, any, bool), next func(string) rpc.ClientEvent) {
	t.Helper()
	evaluate := func(source string) {
		t.Helper()
		if err := terminal.Call(ctx, "session.eval", map[string]any{"id": id, "source": source}, nil); err != nil {
			t.Fatal(err)
		}
	}
	finish := func() { next("eval.result"); next("status") }
	evaluate(`(fs-write "browser-approved.txt" "browser test content")`)
	var prompt event.Confirm
	if err := json.Unmarshal(next("prompt.confirm").Payload, &prompt); err != nil {
		t.Fatal(err)
	}
	if len(prompt.Prompt.Reasons) == 0 || !strings.Contains(prompt.Prompt.Detail, "browser test content") {
		t.Fatal(prompt)
	}
	call("session.answer", map[string]any{"id": "wrong-session", "prompt_id": prompt.ID, "reply": "y"}, nil, true)
	call("session.answer", map[string]any{"id": id, "prompt_id": prompt.ID, "reply": "a"}, nil, true)
	call("session.answer", map[string]any{"id": id, "prompt_id": prompt.ID, "reply": "y", "actor": "forged"}, nil, true)
	call("session.answer", map[string]any{"id": id, "prompt_id": prompt.ID, "reply": "y"}, nil, false)
	receipt := next("guard.decision")
	if !strings.Contains(string(receipt.Payload), `"scope":"once"`) || !strings.Contains(string(receipt.Payload), `unix:uid=`) {
		t.Fatal(string(receipt.Payload))
	}
	finish()
	if raw, err := os.ReadFile(filepath.Join(repo, "browser-approved.txt")); err != nil || string(raw) != "browser test content" {
		t.Fatal("approved write missing", err)
	}
	call("session.answer", map[string]any{"id": id, "prompt_id": prompt.ID, "reply": "y"}, nil, true)
	evaluate(`(fs-write "browser-denied.txt" "must not run")`)
	if err := json.Unmarshal(next("prompt.confirm").Payload, &prompt); err != nil {
		t.Fatal(err)
	}
	call("session.answer", map[string]any{"id": id, "prompt_id": prompt.ID, "reply": "n"}, nil, false)
	finish()
	if _, err := os.Stat(filepath.Join(repo, "browser-denied.txt")); !os.IsNotExist(err) {
		t.Fatal("denied write ran")
	}
	checkBrowserScopes(t, repo, id, evaluate, finish, call, next)
	const canary = "browser-private-canary-not-a-real-secret"
	evaluate(`(define browserCredential (credential-register "browser-test-key"))`)
	var secret event.SecretPrompt
	if err := json.Unmarshal(next("prompt.secret").Payload, &secret); err != nil {
		t.Fatal(err)
	}
	call("session.secret", map[string]any{"id": id, "prompt_id": secret.ID, "secret": canary}, nil, false)
	registered := next("cred.register")
	if !strings.Contains(string(registered.Payload), secret.ID) || !strings.Contains(string(registered.Payload), `unix:uid=`) {
		t.Fatal(string(registered.Payload))
	}
	finish()
	if store.Count() != 1 {
		t.Fatal("missing shared keychain credential")
	}
	var page json.RawMessage
	call("log.read", map[string]any{"id": id, "limit": 1000}, &page, false)
	if strings.Contains(string(page), canary) {
		t.Fatal("credential entered history")
	}
	call("session.secret", map[string]any{"id": id, "prompt_id": secret.ID, "secret": canary}, nil, true)
	received := make(chan string, 1)
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- r.Header.Get("Authorization")
		w.Write([]byte("ok"))
	}))
	defer destination.Close()
	evaluate(fmt.Sprintf(`(http-request browserCredential %q "GET" "header:Authorization:Bearer" '() "")`, destination.URL))
	if err := json.Unmarshal(next("prompt.confirm").Payload, &prompt); err != nil {
		t.Fatal(err)
	}
	if prompt.Options == nil || !prompt.Options.Binding || prompt.Prompt.Binding == nil {
		t.Fatal("missing exact binding choice")
	}
	call("session.binding.create", map[string]any{"id": id, "prompt_id": prompt.ID, "dest": "forged.test"}, nil, true)
	call("session.binding.create", map[string]any{"id": id, "prompt_id": prompt.ID}, nil, false)
	finish()
	if value := <-received; value != "Bearer "+canary {
		t.Fatal("wrong injected credential")
	}
	var info rpc.SessionDetails
	call("session.info", map[string]any{"id": id}, &info, false)
	if len(info.Bindings) != 1 || info.Bindings[0].Dest != prompt.Prompt.Binding.Dest {
		t.Fatal("exact binding not installed")
	}
}

func checkBrowserScopes(t *testing.T, repo, id string, evaluate func(string), finish func(),
	call func(string, any, any, bool), next func(string) rpc.ClientEvent) {
	t.Helper()
	confirm := func() event.Confirm {
		t.Helper()
		var p event.Confirm
		if err := json.Unmarshal(next("prompt.confirm").Payload, &p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	if err := os.MkdirAll(filepath.Join(repo, "notes"), 0700); err != nil {
		t.Fatal(err)
	}
	evaluate(`(fs-write "notes/a" "first")`)
	p := confirm()
	if p.Options == nil || !p.Options.Session || p.Options.Target != "notes/" || !p.Options.Turn {
		t.Fatal("wrong scope options")
	}
	call("session.approval.create", map[string]any{"id": id, "prompt_id": p.ID, "scope": "session"}, nil, false)
	finish()
	evaluate(`(fs-write "notes/b" "standing")`)
	finish()
	evaluate(`(begin (fs-write "turn-a" "first") (fs-write "turn-b" "second"))`)
	p = confirm()
	call("session.approval.create", map[string]any{"id": id, "prompt_id": p.ID, "scope": "turn"}, nil, false)
	finish()
	evaluate(`(fs-write "turn-c" "new turn must ask")`)
	p = confirm()
	call("session.answer", map[string]any{"id": id, "prompt_id": p.ID, "reply": "n"}, nil, false)
	finish()
	if err := os.MkdirAll(filepath.Join(repo, ".nibb"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{`(fs-write ".nibb/allow.toml" "test")`, `(fs-write "security.txt" "sk-ant-abcdefgh1234")`} {
		evaluate(source)
		p = confirm()
		if p.Options == nil || p.Options.Session || p.Options.Turn || p.Options.Binding {
			t.Fatal("protected/security prompt offers widened scope")
		}
		for _, scope := range []string{"session", "turn"} {
			call("session.approval.create", map[string]any{"id": id, "prompt_id": p.ID, "scope": scope}, nil, true)
		}
		call("session.answer", map[string]any{"id": id, "prompt_id": p.ID, "reply": "n"}, nil, false)
		finish()
	}
	for _, path := range []string{"notes/b", "turn-b"} {
		if _, err := os.Stat(filepath.Join(repo, path)); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{"turn-c", "security.txt", ".nibb/allow.toml"} {
		if _, err := os.Stat(filepath.Join(repo, path)); !os.IsNotExist(err) {
			t.Fatal("denied scope wrote a file")
		}
	}
}
