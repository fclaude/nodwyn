package core

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/binding"
	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/keychain"
	"github.com/fclaude/nodwyn/internal/llm"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

type reviewTransport func(*http.Request) (*http.Response, error)

func (f reviewTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestCredentialRequiresSecureTransportByDefault(t *testing.T) {
	st, err := keychain.Create(t.TempDir()+"/keychain", keychain.FastKDF, []byte("fake-passphrase"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.Add("review-key", []byte("review-canary")); err != nil {
		t.Fatal(err)
	}
	sent := false
	previous := egressClient
	egressClient = &http.Client{Transport: reviewTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme != "http" || r.URL.Hostname() != "api.example.test" || r.Header.Get("Authorization") != "Bearer review-canary" {
			t.Fatal("unexpected request")
		}
		sent = true
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("ok")), Request: r}, nil
	})}
	defer func() { egressClient = previous }()
	srv := fake.NewT(t)
	defer srv.Close()
	cfg := config.Default()
	cfg.Control.URL, cfg.Control.Model = srv.URL, "fake"
	var records bytes.Buffer
	s, err := New(Options{Config: cfg, Repo: t.TempDir(), Provider: llm.NewOllama(srv.URL), NoConfirm: true, LogWriter: &records, Keychain: st, Bindings: binding.Table{Bind: []binding.Binding{{Class: "review-key", Dest: "api.example.test", Methods: []string{"POST"}, Positions: []string{"header:Authorization:Bearer"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, err = s.Eval(context.Background(), "(http-request (credential \"review-key\") \"http://api.example.test/resource\" \"POST\" \"header:Authorization:Bearer\" '() \"\")")
	if err == nil || !strings.Contains(err.Error(), "HTTPS") {
		t.Fatalf("cleartext credential was not refused: %v", err)
	}
	if sent {
		t.Fatal("a credential reached the cleartext transport")
	}
}
