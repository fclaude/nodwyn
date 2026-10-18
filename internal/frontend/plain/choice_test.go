package plain

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/binding"
	"github.com/fclaude/nodwyn/internal/keychain"
	fakedrive "github.com/fclaude/nodwyn/test/fakes/drive"
	"github.com/fclaude/nodwyn/test/fakes/must"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

// When the input ends at a question with set answers (how uploads to
// Google Docs are handled, asked the first time), line mode stops the turn
// and returns. It used to wait for the session to decline the question as
// it closed, which it does only once the turn waiting on the answer ends:
// line mode waited forever.
func TestInputEndingAtAChoiceEndsTheTurn(t *testing.T) {
	drive := fakedrive.New()
	defer drive.Close()
	srv := fake.NewT(t, fake.Turn{ToolCalls: []fake.ToolCall{{ID: "a", Name: "google-doc-upload", Args: map[string]any{"path": "notes.md"}}}}, fake.Turn{Content: "stopped"})
	o := testOptions(t, srv.URL)
	o.Config.Docs.GoogleUploadURL = drive.UploadURL()
	must.WriteFile(t, filepath.Join(o.Repo, "notes.md"), []byte("# Notes\n"), 0o644)
	must.MkdirAll(t, o.Paths.ConfigDir, 0o700)
	store, err := keychain.Create(filepath.Join(t.TempDir(), "keychain"), keychain.FastKDF, []byte("passphrase"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Add("google", []byte("ya29.test-only-token")); err != nil {
		t.Fatal(err)
	}
	row, _ := binding.Exact("google", "127.0.0.1", "POST", "header:Authorization:Bearer")
	if err := binding.AppendExact(context.Background(), o.Paths.Bindings, row); err != nil {
		t.Fatal(err)
	}
	if o.Bindings, err = binding.Load(o.Paths.Bindings); err != nil {
		t.Fatal(err)
	}
	o.Keychain = store
	client, _ := testDaemon(t, o)
	id := openTask(t, client, o.Repo)
	out := runChat(t, &Chat{Client: client, ID: id, In: strings.NewReader("upload the notes\n")})
	if !strings.Contains(out, "Uploads to Google Docs") || drive.Requests() != 0 {
		t.Fatalf("the question was not asked, or the upload went:\n%s", out)
	}
}

// With --no-confirm the question is not asked of the script: none of its
// answers only declines, and each is saved for every task, so the turn
// stops and nothing is saved.
func TestNoConfirmStopsAtAChoice(t *testing.T) {
	drive := fakedrive.New()
	defer drive.Close()
	srv := fake.NewT(t, fake.Turn{ToolCalls: []fake.ToolCall{{ID: "a", Name: "google-doc-upload", Args: map[string]any{"path": "notes.md"}}}}, fake.Turn{Content: "after"})
	o := testOptions(t, srv.URL)
	o.Config.Docs.GoogleUploadURL = drive.UploadURL()
	must.WriteFile(t, filepath.Join(o.Repo, "notes.md"), []byte("# Notes\n"), 0o644)
	must.MkdirAll(t, o.Paths.ConfigDir, 0o700)
	store, err := keychain.Create(filepath.Join(t.TempDir(), "keychain"), keychain.FastKDF, []byte("passphrase"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Add("google", []byte("ya29.test-only-token")); err != nil {
		t.Fatal(err)
	}
	row, _ := binding.Exact("google", "127.0.0.1", "POST", "header:Authorization:Bearer")
	if err := binding.AppendExact(context.Background(), o.Paths.Bindings, row); err != nil {
		t.Fatal(err)
	}
	if o.Bindings, err = binding.Load(o.Paths.Bindings); err != nil {
		t.Fatal(err)
	}
	o.Keychain = store
	client, _ := testDaemon(t, o)
	id := openTask(t, client, o.Repo)
	out := runChat(t, &Chat{Client: client, ID: id, NoConfirm: true, In: strings.NewReader("upload the notes\nallow\n")})
	if !strings.Contains(out, "--no-confirm: none of these answers only declines; the turn stops") || drive.Requests() != 0 {
		t.Fatalf("the question was answered, or the upload went:\n%s", out)
	}
	if conf, err := os.ReadFile(o.Paths.ConfigFile); err == nil && strings.Contains(string(conf), "google_upload") {
		t.Fatalf("an answer was saved:\n%s", conf)
	}
}
