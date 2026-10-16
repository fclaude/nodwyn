package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

func TestAttachmentReachesTheModelAsRedactedData(t *testing.T) {
	t.Parallel()
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

// A long name is cut to 120 bytes keeping its extension; a long "extension"
// is cut with the rest, and no cut splits a rune. A name with an extension
// past 120 bytes used to slice out of range, a panic that ended the daemon.
func TestCleanNameCutsLongNames(t *testing.T) {
	for _, name := range []string{
		"a." + strings.Repeat("x", 200),
		strings.Repeat("ü", 100) + ".png",
		strings.Repeat("y", 300) + ".tar.gz",
	} {
		got := cleanName(name, "")
		if len(got) > 120 || !utf8.ValidString(got) {
			t.Errorf("%.20q… became %q (%d bytes)", name, got, len(got))
		}
	}
	if got := cleanName(strings.Repeat("ü", 100)+".png", ""); !strings.HasSuffix(got, ".png") {
		t.Errorf("the extension was lost: %q", got)
	}
}

// A repository that commits .nodwyn as a link to somewhere else does not
// get a copy written there: the copy and its .gitignore went where the link
// pointed, outside the repository.
func TestAttachWritesOnlyInTheRepositorysOwnFolder(t *testing.T) {
	t.Parallel()
	r := newRig(t, false)
	elsewhere := t.TempDir()
	if err := os.RemoveAll(filepath.Join(r.repo, ".nodwyn")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(r.repo, ".nodwyn")); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(t.TempDir(), "report.txt")
	if err := os.WriteFile(src, []byte("report\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.s.Attach(src, "", "unix:test"); err == nil {
		t.Fatal("attached through a linked .nodwyn")
	}
	if entries, _ := os.ReadDir(elsewhere); len(entries) != 0 {
		t.Fatalf("written where the link points: %v", entries)
	}
}
