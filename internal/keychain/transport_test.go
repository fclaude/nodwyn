package keychain

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"
)

func TestCredentialSinksRequireSecureTransport(t *testing.T) {
	st, err := Create(filepath.Join(t.TempDir(), "keychain"), FastKDF, []byte("test-only"))
	if err != nil {
		t.Fatal(err)
	}
	h, err := st.Add("test-key", []byte("test-canary"))
	if err != nil {
		t.Fatal(err)
	}
	for _, sink := range []func(*http.Request) error{
		func(r *http.Request) error { return st.InjectHeader(h.ID, "Authorization", "Bearer ", r) },
		func(r *http.Request) error { return st.InjectQuery(h.ID, "key", r) },
		func(r *http.Request) error { return st.InjectBody(h.ID, r) },
	} {
		for _, endpoint := range []string{"http://api.example.test/x", "ftp://api.example.test/x", "https://user:pass@api.example.test/x"} {
			r, err := http.NewRequest("POST", endpoint, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := sink(r); err == nil {
				t.Errorf("credential reached %s", endpoint)
			}
			if r.Header.Get("Authorization") != "" || r.URL.RawQuery != "" || r.Body != nil {
				t.Fatal("refusal left a credential in the request")
			}
		}
	}
}

func TestInsecureTransportIsAnExplicitHostSetting(t *testing.T) {
	for _, tc := range []struct {
		url         string
		allow, want bool
	}{
		{"https://api.example.test", false, true}, {"http://127.0.0.1", false, true}, {"http://[::1]", false, true}, {"http://localhost", false, true},
		{"http://api.example.test", false, false}, {"http://api.example.test", true, true}, {"ftp://api.example.test", true, false},
		{"http://localhost.attacker.test", false, false}, {"http://192.168.1.20", false, false},
	} {
		req, err := http.NewRequestWithContext(WithInsecureHTTP(context.Background(), tc.allow), "POST", tc.url, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := CheckTransport(req) == nil; got != tc.want {
			t.Errorf("%s (allow=%v): %v", tc.url, tc.allow, got)
		}
	}
}
