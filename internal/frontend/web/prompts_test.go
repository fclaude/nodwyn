package web

import (
	"context"
	"encoding/json"
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
	const canary = "browser-private-canary-not-a-real-secret"
	evaluate(`(credential-register "browser-test-key")`)
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
}
