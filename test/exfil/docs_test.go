package exfil

import (
	"archive/zip"
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/binding"
	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/core"
	"github.com/fclaude/nodwyn/internal/keychain"
	fakedrive "github.com/fclaude/nodwyn/test/fakes/drive"
	"github.com/fclaude/nodwyn/test/fakes/must"
)

// Documents out (W5): a document sent to Google Drive never was a Scheme
// value, so the guard cannot read it. These hold the handler's own check
// and the binding: a document carrying the canary, wherever in the file it
// hides, never reaches Drive, an unbound upload never leaves, and neither
// the canary nor the token reaches the log. Uploads are set to always
// allow, so nobody is asked and only refusals stop them.

const googleSecret = "ya29.exfil-google-token-0123456789"

func docsSession(t *testing.T, drive *fakedrive.Server, bound bool) (*core.Session, string, *strings.Builder) {
	t.Helper()
	kc, err := keychain.Create(filepath.Join(t.TempDir(), "kc"), keychain.FastKDF, []byte("test-pass"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kc.Add("google", []byte(googleSecret)); err != nil {
		t.Fatal(err)
	}
	var tbl binding.Table
	if bound {
		tbl.Bind = []binding.Binding{{Class: "google", Dest: "127.0.0.1", Positions: []string{"header:Authorization:Bearer"}, Methods: []string{"POST"}}}
	}
	cfg := config.Default()
	cfg.Docs.GoogleUploadURL = drive.UploadURL()
	cfg.Docs.GoogleUpload = config.UploadAllow
	repo := t.TempDir()
	logb := &strings.Builder{}
	s, err := core.New(core.Options{Config: cfg, Repo: repo, AutoApprove: true, LogWriter: logb, Keychain: kc, Bindings: tbl})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for range s.Events() {
		}
	}()
	t.Cleanup(func() { s.Close() })
	return s, repo, logb
}

// wordFile is a .docx whose parts are given.
func wordFile(t *testing.T, parts map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, body := range parts {
		f, _ := w.Create(name)
		f.Write([]byte(body))
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestDocumentHoldingTheCanaryNeverReachesDrive(t *testing.T) {
	drive := fakedrive.New()
	t.Cleanup(drive.Close)
	s, repo, logb := docsSession(t, drive, true)
	half := len(canary) / 2
	files := map[string][]byte{
		"body.docx":    wordFile(t, map[string]string{"word/document.xml": `<w:document><w:body><w:p><w:r><w:t>` + canary[:half] + `</w:t></w:r><w:r><w:t>` + canary[half:] + `</w:t></w:r></w:p></w:body></w:document>`}),
		"comment.docx": wordFile(t, map[string]string{"word/document.xml": `<w:document/>`, "word/comments.xml": `<w:comments><w:comment><w:p><w:r><w:t>` + canary + `</w:t></w:r></w:p></w:comment></w:comments>`}),
		"meta.docx":    wordFile(t, map[string]string{"word/document.xml": `<w:document/>`, "docProps/core.xml": `<cp:coreProperties><dc:description>` + canary + `</dc:description></cp:coreProperties>`}),
		"hidden.docx":  wordFile(t, map[string]string{"word/document.xml": `<w:document/>`, "word/embeddings/oleObject1.bin": canary}),
		"page.html":    []byte(`<p><a href="https://example.com/?k=` + canary + `">clean</a></p>`),
		"notes.txt":    []byte("notes " + canary + "\n"),
	}
	for name, content := range files {
		must.WriteFile(t, filepath.Join(repo, name), content, 0o644)
		_, err := s.Eval(context.Background(), `(google-doc-upload "`+name+`")`)
		if err == nil || !strings.Contains(err.Error(), "nothing was sent") {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if drive.Requests() != 0 {
		t.Fatalf("the canary reached Drive: %+v", drive.Uploads())
	}
	if strings.Contains(logb.String(), canary) || strings.Contains(logb.String(), googleSecret) {
		t.Fatal("the canary or the token reached the log")
	}
	// The same upload without the canary goes, with the bound token.
	must.WriteFile(t, filepath.Join(repo, "clean.txt"), []byte("notes\n"), 0o644)
	if _, err := s.Eval(context.Background(), `(google-doc-upload "clean.txt")`); err != nil {
		t.Fatal(err)
	}
	if ups := drive.Uploads(); len(ups) != 1 || ups[0].Auth != "Bearer "+googleSecret {
		t.Fatalf("uploads: %+v", ups)
	}
	if strings.Contains(logb.String(), googleSecret) {
		t.Fatal("the token reached the log")
	}
}

func TestUnboundUploadNeverLeaves(t *testing.T) {
	drive := fakedrive.New()
	t.Cleanup(drive.Close)
	s, repo, logb := docsSession(t, drive, false)
	must.WriteFile(t, filepath.Join(repo, "notes.txt"), []byte("notes\n"), 0o644)
	_, err := s.Eval(context.Background(), `(google-doc-upload "notes.txt")`)
	if err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("an unbound upload: %v", err)
	}
	if drive.Requests() != 0 || strings.Contains(logb.String(), googleSecret) {
		t.Fatal("an unbound upload left, or its token reached the log")
	}
}
