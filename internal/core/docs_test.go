package core

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fclaude/nodwyn/internal/binding"
	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/event"
	"github.com/fclaude/nodwyn/internal/keychain"
	"github.com/fclaude/nodwyn/internal/llm"
	"github.com/fclaude/nodwyn/internal/log"
	fakedrive "github.com/fclaude/nodwyn/test/fakes/drive"
	fakeoauth "github.com/fclaude/nodwyn/test/fakes/oauth"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

const (
	googleToken = "ya29.static-google-canary-0123456789"
	docxType    = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
)

// docx builds a minimal Word document whose parts are given; the body is
// word/document.xml.
func docx(t *testing.T, parts map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, body := range map[string]string{"[Content_Types].xml": `<?xml version="1.0"?><Types/>`} {
		f, _ := w.Create(name)
		f.Write([]byte(body))
	}
	for name, body := range parts {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		f.Write([]byte(body))
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// wordBody is a document.xml with one paragraph of runs.
func wordBody(runs ...string) string {
	var b strings.Builder
	b.WriteString(`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p>`)
	for _, r := range runs {
		b.WriteString("<w:r><w:t>" + r + "</w:t></w:r>")
	}
	b.WriteString(`</w:p></w:body></w:document>`)
	return b.String()
}

type docsSetup struct {
	oauth   *fakeoauth.Server // an OAuth2 credential, stale when the session opens; nil: a static key
	noKey   bool              // store no google credential
	unbound bool              // no binding row for the upload host
	turns   []fake.Turn
	upload  string         // docs.google_upload; "" is "ask", "unset" leaves it unset
	adjust  func(*Options) // last changes to the session's options
}

// docsRig opens a session whose google credential is bound (or not) to the
// fake Drive's host, with the upload endpoint pointed at the fake.
func docsRig(t *testing.T, drive *fakedrive.Server, setup docsSetup) (*rig, config.Paths) {
	t.Helper()
	root := t.TempDir()
	repo := filepath.Join(root, "work")
	os.MkdirAll(repo, 0o755)
	paths := config.PathsUnder(filepath.Join(root, "config"), filepath.Join(root, "data"))
	os.MkdirAll(paths.ConfigDir, 0o700)
	store, err := keychain.Create(filepath.Join(root, "keychain"), keychain.FastKDF, []byte("passphrase"))
	if err != nil {
		t.Fatal(err)
	}
	var scopes []string
	switch {
	case setup.noKey:
	case setup.oauth != nil:
		now := time.Now()
		store.SetClock(func() time.Time { return now })
		g := keychain.Grant{Profile: keychain.Custom(setup.oauth.AuthURL(), setup.oauth.TokenURL(), ""), ClientID: setup.oauth.ClientID, Scopes: []string{"drive.file"}}
		if _, err := store.AddOAuth2(context.Background(), "google", g, setup.oauth.Browser); err != nil {
			t.Fatal(err)
		}
		stale := now.Add(2 * time.Hour)
		store.SetClock(func() time.Time { return stale })
		scopes = []string{"drive.file"}
	default:
		store.Add("google", []byte(googleToken))
	}
	if !setup.unbound {
		row, _ := binding.Exact("google", "127.0.0.1", "POST", "header:Authorization:Bearer", scopes...)
		if err := binding.AppendExact(context.Background(), paths.Bindings, row); err != nil {
			t.Fatal(err)
		}
	}
	srv := fake.NewT(t, setup.turns...)
	t.Cleanup(srv.Close)
	cfg := config.Default()
	cfg.Control.URL, cfg.Control.Model = srv.URL, "test-model"
	cfg.Docs.GoogleUploadURL = drive.UploadURL()
	switch setup.upload {
	case "":
		cfg.Docs.GoogleUpload = config.UploadAsk
	case "unset":
	default:
		cfg.Docs.GoogleUpload = setup.upload
	}
	logb := &bytes.Buffer{}
	o := Options{Config: cfg, Repo: repo, Paths: paths, Keychain: store, Version: "test", Provider: llm.NewOllama(srv.URL), LogWriter: logb}
	if setup.adjust != nil {
		setup.adjust(&o)
	}
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return &rig{t: t, s: s, srv: srv, repo: repo, logb: logb}, paths
}

func uploadCall(id, path, title string) fake.Turn {
	args := map[string]any{"path": path}
	if title != "" {
		args["title"] = title
	}
	return fake.Turn{ToolCalls: []fake.ToolCall{{ID: id, Name: "google-doc-upload", Args: args}}}
}

func (r *rig) docUploads() []log.DocUpload {
	r.t.Helper()
	var out []log.DocUpload
	for _, ev := range r.logged() {
		var d log.DocUpload
		if ev.Kind == log.KindDocUpload && ev.Decode(&d) == nil {
			out = append(out, d)
		}
	}
	return out
}

func toolNames(s *Session) string {
	var names []string
	for _, tool := range s.Tools() {
		names = append(names, tool.Name)
	}
	return " " + strings.Join(names, " ") + " "
}

// google-doc-upload is offered only to a session that opens with a google
// credential stored, since the tool list is fixed for the session;
// doc-export is offered to every one.
func TestGoogleDocUploadOfferedOnlyWithACredential(t *testing.T) {
	t.Parallel()
	drive := fakedrive.New()
	t.Cleanup(drive.Close)
	with, _ := docsRig(t, drive, docsSetup{})
	without, _ := docsRig(t, drive, docsSetup{noKey: true})
	plain := newRig(t, true)
	if got := toolNames(with.s); !strings.Contains(got, " google-doc-upload ") || !strings.Contains(got, " doc-export ") {
		t.Fatalf("with a google credential: %s", got)
	}
	for _, r := range []*rig{without, plain} {
		if got := toolNames(r.s); strings.Contains(got, "google-doc-upload") || !strings.Contains(got, " doc-export ") {
			t.Fatalf("without a google credential: %s", got)
		}
	}
}

// A bound upload asks, naming the file, its size and the destination, with
// no standing answer; then sends one multipart request carrying the stored
// key, the metadata asking for a Google Doc, and the file's bytes, returns
// the document's id and URL, and records doc.upload. No key in the log.
func TestGoogleDocUploadSendsTheDocument(t *testing.T) {
	t.Parallel()
	drive := fakedrive.New()
	t.Cleanup(drive.Close)
	r, _ := docsRig(t, drive, docsSetup{turns: []fake.Turn{uploadCall("u", "report.docx", "Q3 report"), {Content: "uploaded"}}})
	content := docx(t, map[string]string{"word/document.xml": wordBody("Quarterly ", "numbers")})
	os.WriteFile(filepath.Join(r.repo, "report.docx"), content, 0o644)
	r.s.Send("put the report in Google Docs")
	var confirms []event.Confirm
	for _, e := range r.drain("y") {
		if e.Kind == event.PromptConfirm {
			confirms = append(confirms, e.Payload.(event.Confirm))
		}
	}
	if len(confirms) != 1 {
		t.Fatalf("confirms: %+v", confirms)
	}
	p := confirms[0].Prompt
	if p.Prim != "google-doc-upload" || p.Egress == nil || p.Egress.Class != "google" || p.Egress.Host != "127.0.0.1" || p.Path != "" {
		t.Fatalf("prompt: %+v", p)
	}
	for _, want := range []string{"report.docx", "Word document", "B)", "to 127.0.0.1", `"Q3 report"`} {
		if !strings.Contains(p.Detail, want) {
			t.Fatalf("preview %q lacks %q", p.Detail, want)
		}
	}
	if !strings.Contains(strings.Join(p.Reasons, "; "), "sends a file out of this machine") {
		t.Fatalf("reasons: %v", p.Reasons)
	}
	if o := confirms[0].Options; o == nil || o.Session || o.Turn {
		t.Fatalf("an upload offered a standing answer: %+v", o)
	}
	ups := drive.Uploads()
	if len(ups) != 1 {
		t.Fatalf("uploads: %+v", ups)
	}
	up := ups[0]
	if up.Auth != "Bearer "+googleToken || up.Query.Get("uploadType") != "multipart" {
		t.Fatalf("upload request: auth %q query %v", up.Auth, up.Query)
	}
	if up.Metadata["name"] != "Q3 report" || up.Metadata["mimeType"] != "application/vnd.google-apps.document" || up.MediaType != docxType || !bytes.Equal(up.Media, content) {
		t.Fatalf("upload body: %+v (%d bytes)", up.Metadata, len(up.Media))
	}
	result := r.results()
	if result["u"] != "" {
		t.Fatalf("the upload failed: %s", result["u"])
	}
	recs := r.docUploads()
	if len(recs) != 1 || !recs[0].OK || !recs[0].Sent || recs[0].ID != up.ID || recs[0].URL != "https://docs.google.com/document/d/"+up.ID+"/edit" ||
		recs[0].Bytes != len(content) || len(recs[0].SHA256) != 64 || recs[0].Host != "127.0.0.1" || recs[0].Class != "google" || recs[0].Origin != "model" {
		t.Fatalf("doc.upload: %+v", recs)
	}
	if strings.Contains(r.logb.String(), googleToken) || strings.Contains(string(r.srv.Requests[1].Raw), googleToken) {
		t.Fatal("the key reached the log or the model")
	}
	if !strings.Contains(string(r.srv.Requests[1].Raw), up.ID) {
		t.Fatal("the model did not get the document's id")
	}
}

// Without a binding for the upload host, the guard denies the call before
// anything is asked or sent.
func TestGoogleDocUploadUnboundIsDenied(t *testing.T) {
	t.Parallel()
	drive := fakedrive.New()
	t.Cleanup(drive.Close)
	r, _ := docsRig(t, drive, docsSetup{unbound: true, turns: []fake.Turn{uploadCall("u", "report.docx", ""), {Content: "refused"}}})
	os.WriteFile(filepath.Join(r.repo, "report.docx"), docx(t, map[string]string{"word/document.xml": wordBody("hello")}), 0o644)
	r.s.Send("upload it")
	for _, e := range r.drain("y") {
		if e.Kind == event.PromptConfirm {
			t.Fatalf("an unbound upload asked: %+v", e.Payload)
		}
	}
	if drive.Requests() != 0 {
		t.Fatal("an unbound upload reached Drive")
	}
	ds := r.mcpDecisions("google-doc-upload")
	if len(ds) != 1 || ds[0].Decision != "deny" || ds[0].Checker != "egress" {
		t.Fatalf("decisions: %+v", ds)
	}
	if len(r.docUploads()) != 0 {
		t.Fatal("a denied call reached the handler")
	}
}

// A document holding a key is refused before anything is sent: the guard
// never saw its bytes, so the handler scans them, in every part of the
// package and across the runs Word splits text into. The same key bound
// for the upload host may go, as for any payload.
func TestGoogleDocUploadRefusesADocumentHoldingAKey(t *testing.T) {
	t.Parallel()
	const key = "sk-ant-api03-AbCdEfGhIjKlMnOpQrStUv"
	drive := fakedrive.New()
	t.Cleanup(drive.Close)
	r, paths := docsRig(t, drive, docsSetup{turns: []fake.Turn{
		{ToolCalls: []fake.ToolCall{
			{ID: "split", Name: "google-doc-upload", Args: map[string]any{"path": "split.docx"}},
			{ID: "header", Name: "google-doc-upload", Args: map[string]any{"path": "header.docx"}},
			{ID: "html", Name: "google-doc-upload", Args: map[string]any{"path": "page.html"}},
		}},
		{Content: "refused"},
		uploadCall("again", "split.docx", ""),
		{Content: "sent"},
	}})
	os.WriteFile(filepath.Join(r.repo, "split.docx"), docx(t, map[string]string{"word/document.xml": wordBody("token sk-ant-", "api03-AbCdEfGhIjKlMnOpQrStUv")}), 0o644)
	os.WriteFile(filepath.Join(r.repo, "header.docx"), docx(t, map[string]string{"word/document.xml": wordBody("clean"), "word/header1.xml": wordBody(key)}), 0o644)
	os.WriteFile(filepath.Join(r.repo, "page.html"), []byte(`<p data-k="`+key+`">clean</p>`), 0o644)
	r.s.Send("upload them")
	r.drain("y")
	if drive.Requests() != 0 {
		t.Fatalf("a key reached Drive: %+v", drive.Uploads())
	}
	results := r.results()
	for _, id := range []string{"split", "header", "html"} {
		if !strings.Contains(results[id], "nothing was sent") || !strings.Contains(results[id], "anthropic-key") {
			t.Fatalf("%s: %q", id, results[id])
		}
	}
	recs := r.docUploads()
	if len(recs) != 3 || recs[0].Sent || recs[0].OK || !strings.Contains(recs[0].Error, "anthropic-key") {
		t.Fatalf("doc.upload: %+v", recs)
	}
	if strings.Contains(r.logb.String(), "AbCdEfGhIjKlMnOpQrStUv") {
		t.Fatal("the key reached the log")
	}
	row, _ := binding.Exact("anthropic-key", "127.0.0.1", "POST", "header:x-api-key")
	if err := binding.AppendExact(context.Background(), paths.Bindings, row); err != nil {
		t.Fatal(err)
	}
	r.s.Send("again")
	r.drain("y")
	if ups := drive.Uploads(); len(ups) != 1 || ups[0].Metadata["name"] != "split" {
		t.Fatalf("bound: %+v", ups)
	}
}

// An OAuth2 credential stale when the session opens is refreshed just
// before the upload, through the session's sink, and the refresh is
// logged; Drive gets the fresh token, and no token reaches the log.
func TestGoogleDocUploadRefreshesAStaleOAuth2Token(t *testing.T) {
	t.Parallel()
	provider := fakeoauth.New("nodwyn-test")
	t.Cleanup(provider.Close)
	drive := fakedrive.New()
	t.Cleanup(drive.Close)
	r, _ := docsRig(t, drive, docsSetup{oauth: provider, turns: []fake.Turn{uploadCall("u", "notes.txt", ""), {Content: "uploaded"}}})
	os.WriteFile(filepath.Join(r.repo, "notes.txt"), []byte("meeting notes\n"), 0o644)
	r.s.Send("upload the notes")
	var scopes []string
	for _, e := range r.drain("y") {
		if e.Kind == event.PromptConfirm {
			scopes = e.Payload.(event.Confirm).Prompt.Egress.Scopes
		}
	}
	if len(scopes) != 1 || !strings.HasSuffix(scopes[0], "drive.file") {
		t.Fatalf("the confirm named scopes %v", scopes)
	}
	if provider.Refreshes() != 1 {
		t.Fatalf("want one refresh, got %d", provider.Refreshes())
	}
	ups := drive.Uploads()
	if len(ups) != 1 || !strings.HasPrefix(ups[0].Auth, "Bearer fake-access-token-") || ups[0].MediaType != "text/plain" || string(ups[0].Media) != "meeting notes\n" {
		t.Fatalf("uploads: %+v", ups)
	}
	var refreshed bool
	for _, ev := range r.logged() {
		var rec log.CredRefresh
		if ev.Kind == log.KindCredRefresh && ev.Decode(&rec) == nil {
			refreshed = rec.OK && rec.Class == "google"
		}
	}
	if !refreshed {
		t.Fatal("the refresh was not logged")
	}
	if fakeoauth.HasToken(r.logb.String()) {
		t.Fatal("a token reached the log")
	}
}

// Drive's refusal (a token without the scope it needs) is the tool's
// error, in Drive's words, and doc.upload says the request was sent.
func TestGoogleDocUploadReportsDrivesRefusal(t *testing.T) {
	t.Parallel()
	drive := fakedrive.New()
	t.Cleanup(drive.Close)
	drive.Refuse(403)
	r, _ := docsRig(t, drive, docsSetup{turns: []fake.Turn{uploadCall("u", "notes.txt", ""), {Content: "refused"}}})
	os.WriteFile(filepath.Join(r.repo, "notes.txt"), []byte("notes\n"), 0o644)
	r.s.Send("upload the notes")
	r.drain("y")
	if got := r.results()["u"]; !strings.Contains(got, "403") || !strings.Contains(got, "insufficient authentication scopes") {
		t.Fatalf("result: %q", got)
	}
	if recs := r.docUploads(); len(recs) != 1 || !recs[0].Sent || recs[0].OK {
		t.Fatalf("doc.upload: %+v", recs)
	}
}
