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

func TestUserSettingAllowsOnlyBoundRemoteHTTP(t *testing.T) {
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
	cfg, err := config.Parse([]byte("[policy]\nallow_insecure_http = true\n"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Control.URL, cfg.Control.Model = srv.URL, "fake"
	var records bytes.Buffer
	s, err := New(Options{Config: cfg, Repo: t.TempDir(), Provider: llm.NewOllama(srv.URL), NoConfirm: true, LogWriter: &records, Keychain: st, Bindings: binding.Table{Bind: []binding.Binding{{Class: "review-key", Dest: "api.example.test", Methods: []string{"POST"}, Positions: []string{"header:Authorization:Bearer"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, err = s.Eval(context.Background(), "(http-request (credential \"review-key\") \"http://api.example.test/resource\" \"POST\" \"header:Authorization:Bearer\" '() \"\")")
	if err != nil || !sent {
		t.Fatalf("explicit setting did not allow bound HTTP: %v", err)
	}
	sent = false
	_, err = s.Eval(context.Background(), `(http-request (credential "review-key") "http://unbound.example.test/resource" "POST" "header:Authorization:Bearer" '() "")`)
	if err == nil || sent {
		t.Fatalf("the transport exception bypassed binding policy: %v", err)
	}
}
