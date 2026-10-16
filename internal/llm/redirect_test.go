package llm

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// A model server that redirects gets nothing further sent: not to the host
// it points at, which no binding and no config names. Go's client would send
// a key in a custom header there, and a 307 or 308 would send the window.
func TestModelClientsRefuseRedirects(t *testing.T) {
	const key, window = "sk-redirect-key-canary-5150", "window-canary-5150"
	var mu sync.Mutex
	var elsewhere []string // every request the other host got, as sent
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dump, _ := httputil.DumpRequest(r, true)
		mu.Lock()
		elsewhere = append(elsewhere, string(dump))
		mu.Unlock()
	}))
	defer other.Close()
	authorize := func(r *http.Request) error {
		r.Header.Set("Authorization", "Bearer "+key)
		r.Header.Set("X-Api-Key", key) // a custom position: Go never drops it
		return nil
	}
	for _, status := range []int{http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		for name, provider := range map[string]func(url string) Provider{
			"openai": func(url string) Provider { return NewOpenAI(url, authorize) },
			"ollama": func(url string) Provider { return NewOllama(url) },
		} {
			t.Run(name+"/"+http.StatusText(status), func(t *testing.T) {
				mu.Lock()
				elsewhere = nil
				mu.Unlock()
				var sent atomic.Int32
				bound := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					sent.Add(1)
					http.Redirect(w, r, other.URL+"/v1/chat/completions", status)
				}))
				defer bound.Close()
				_, err := provider(bound.URL).Chat(context.Background(), Request{Model: "m", Messages: []Message{{Role: RoleUser, Content: window}}}, nil)
				want := "redirected to " + strings.TrimPrefix(other.URL, "http://") + "; nodwyn does not follow redirects for model requests"
				if err == nil || !strings.Contains(err.Error(), want) {
					t.Fatalf("err %v, want it to say %q", err, want)
				}
				if !errors.As(err, new(permanentError)) {
					t.Fatalf("a redirect would be asked again: %v", err)
				}
				if n := sent.Load(); n != 1 {
					t.Fatalf("the bound server got %d requests", n)
				}
				mu.Lock()
				defer mu.Unlock()
				for _, req := range elsewhere {
					if strings.Contains(req, key) {
						t.Errorf("the key reached the other host:\n%s", req)
					}
					if strings.Contains(req, window) {
						t.Errorf("the window reached the other host:\n%s", req)
					}
				}
				if len(elsewhere) != 0 {
					t.Fatalf("the redirect was followed: the other host got %d requests", len(elsewhere))
				}
			})
		}
	}
}

// Only a redirect is refused: another status, or a 3xx without a Location,
// is the caller's to handle, and the error names the host, not the address,
// which could repeat a key sent in the query.
func TestRedirectedNamesOnlyTheHost(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "https://api.example.test/v1?key=sk-query-canary", nil)
	resp := func(status int, location string) *http.Response {
		h := http.Header{}
		if location != "" {
			h.Set("Location", location)
		}
		return &http.Response{StatusCode: status, Header: h, Request: req}
	}
	if err := Redirected(resp(http.StatusOK, ""), "keyed requests"); err != nil {
		t.Fatal(err)
	}
	if err := Redirected(resp(http.StatusNotModified, "https://elsewhere.test/"), "keyed requests"); err != nil {
		t.Fatal(err)
	}
	if err := Redirected(resp(http.StatusFound, ""), "keyed requests"); err != nil {
		t.Fatal(err)
	}
	err := Redirected(resp(http.StatusTemporaryRedirect, "https://elsewhere.test:8443/v1?key=sk-query-canary"), "keyed requests")
	if err == nil || err.Error() != "the server redirected to elsewhere.test:8443; nodwyn does not follow redirects for keyed requests" {
		t.Fatalf("err %v", err)
	}
	// A relative Location is the same host, and still not followed.
	if err := Redirected(resp(http.StatusMovedPermanently, "/v2"), "keyed requests"); err == nil || !strings.Contains(err.Error(), "redirected to api.example.test;") {
		t.Fatalf("err %v", err)
	}
}

// The calls that are not chat refuse a redirect too, for every provider, as
// one function sends them all (P2-070): Ollama's Ping read a redirect as a
// bare status, and its model check as a plain error that a retry would repeat.
func TestEveryCallRefusesARedirect(t *testing.T) {
	bound := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://other.invalid/", http.StatusTemporaryRedirect)
	}))
	defer bound.Close()
	ctx := context.Background()
	ollama, openai, anthropic := NewOllama(bound.URL), NewOpenAI(bound.URL, nil), NewAnthropic(bound.URL, nil)
	calls := map[string]func() error{
		"ollama ping":         func() error { _, err := ollama.Ping(ctx); return err },
		"ollama capabilities": func() error { _, err := ollama.Capabilities(ctx, "m"); return err },
		"openai ping":         func() error { _, err := openai.Ping(ctx); return err },
		"openai models":       func() error { _, err := openai.Models(ctx); return err },
		"anthropic ping":      func() error { _, err := anthropic.Ping(ctx); return err },
		"anthropic models":    func() error { _, err := anthropic.Models(ctx); return err },
	}
	for name, call := range calls {
		err := call()
		if err == nil || !strings.Contains(err.Error(), "redirected to other.invalid") || !errors.As(err, new(permanentError)) {
			t.Errorf("%s: %v", name, err)
		}
	}
}
