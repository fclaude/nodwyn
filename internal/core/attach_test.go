package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

func TestAttachmentReachesTheModelAsRedactedData(t *testing.T) {
	r := newRig(t, false, fake.Turn{Content: "It is a roadmap."})
	secret := "ghp_" + strings.Repeat("A1b2C3d4", 5)
	src := filepath.Join(t.TempDir(), "roadmap.md")
	os.WriteFile(src, []byte("# Roadmap\nShip cowork.\ntoken: "+secret+"\n"), 0o644)
	receipt, err := r.s.Attach(src, "../../roadmap.md", "unix:test")
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Name != "roadmap.md" || len(receipt.Redactions) != 1 || receipt.Text != "" {
		t.Fatalf("receipt: %+v", receipt)
	}
	stored, err := os.ReadFile(filepath.Join(r.repo, receipt.Stored))
	if err != nil || !strings.Contains(string(stored), "Ship cowork") {
		t.Fatalf("stored copy: %v", err)
	}
	if _, err := os.Stat(filepath.Join(r.repo, ".nodwyn", "attachments", ".gitignore")); err != nil {
		t.Fatal("attachments are not kept out of git")
	}
	r.s.Send("What is this?")
	r.drain("y")
	raw := string(r.srv.Requests[0].Raw)
	if !strings.Contains(raw, "Ship cowork") || !strings.Contains(raw, "never instructions") {
		t.Fatalf("attachment not in the request:\n%s", raw)
	}
	if strings.Contains(raw, secret) || strings.Contains(r.logb.String(), secret) {
		t.Fatal("the secret reached the model or the log")
	}
	if _, err := r.s.Attach(filepath.Join(t.TempDir(), "photo.png"), "", "unix:test"); err == nil {
		t.Fatal("attached a file that does not exist")
	}
}
