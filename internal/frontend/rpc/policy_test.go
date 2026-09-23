package rpc

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/fclaude/nibb/internal/event"
)

func TestPolicyRPCRequiresReviewAndRejectsCallerAuthority(t *testing.T) {
	path, _ := start(t, options(t))
	c := connect(t, path)
	c.hello()
	repo := t.TempDir()
	id := opened(t, c, repo)
	subscribed(t, c, id, 0, 100)
	p := map[string]any{"id": id, "name": "approvals"}
	c.ok("session.policy.read", p)
	doc := prompt[event.Policy](t, c, event.PolicyResult)
	c.until(event.Status)
	if doc.Err != "" || doc.Revision != "missing" {
		t.Fatal(doc)
	}
	content := "commands=['go test']\n"
	for _, extra := range []string{"path", "actor"} {
		bad := map[string]any{"id": id, "name": "approvals", "revision": doc.Revision, "content": content, extra: "forged"}
		if c.call("session.policy.write", bad).Error == nil {
			t.Fatal("accepted", extra)
		}
	}
	c.ok("session.policy.write", map[string]any{"id": id, "name": "approvals", "revision": doc.Revision, "content": content})
	confirm := prompt[event.Confirm](t, c, event.PromptConfirm)
	if _, err := os.Stat(filepath.Join(repo, ".nibb", "allow.toml")); !os.IsNotExist(err) {
		t.Fatal("wrote before review")
	}
	c.ok("session.answer", map[string]any{"id": id, "prompt_id": confirm.ID, "reply": "y"})
	result := prompt[event.Policy](t, c, event.PolicyResult)
	c.until(event.Status)
	if !result.Saved || result.Err != "" {
		t.Fatal(result)
	}
	got, err := os.ReadFile(filepath.Join(repo, ".nibb", "allow.toml"))
	if err != nil || string(got) != content {
		t.Fatal(string(got), err)
	}
}
