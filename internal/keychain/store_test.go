package keychain

import (
	"net/http"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/scheme"
)

// newStore makes a keychain at a temp path with fast KDF params (tests must
// not run the production argon2id cost).
func newStore(t *testing.T, pass []byte) *Store {
	t.Helper()
	st, err := Create(t.TempDir()+"/keychain", FastKDF, pass)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestRoundTrip(t *testing.T) {
	st := newStore(t, []byte("correct horse battery staple"))
	h, err := st.Add("openai-key", []byte("sk-test-12345"))
	if err != nil {
		t.Fatal(err)
	}
	if h.Class != "openai-key" || h.ID == "" {
		t.Fatalf("handle: %+v", h)
	}
	if st.Count() != 1 {
		t.Fatalf("count: %d", st.Count())
	}

	// Re-open the same file with the correct passphrase.
	path := st.path
	st2, err := Open(path, []byte("correct horse battery staple"))
	if err != nil {
		t.Fatal(err)
	}
	if st2.Count() != 1 {
		t.Fatalf("re-open count: %d", st2.Count())
	}
	class, _, err := st2.Lookup(h.ID)
	if err != nil || class != "openai-key" {
		t.Fatalf("lookup: %q %v", class, err)
	}
}

func TestWrongPassphrase(t *testing.T) {
	st := newStore(t, []byte("right"))
	if _, err := Open(st.path, []byte("wrong")); err == nil || !strings.Contains(err.Error(), "wrong passphrase") {
		t.Fatalf("wrong passphrase: %v", err)
	}
}

func TestInjectHeader(t *testing.T) {
	st := newStore(t, []byte("pass"))
	h, _ := st.Add("api-key", []byte("secret-value-123"))

	req, _ := http.NewRequest("GET", "https://api.example.com/v1", nil)
	if err := st.InjectHeader(h.ID, "Authorization", "Bearer ", req); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer secret-value-123" {
		t.Fatalf("header: %q", got)
	}
}

func TestInjectQuery(t *testing.T) {
	st := newStore(t, []byte("pass"))
	h, _ := st.Add("api-key", []byte("secret-value-123"))

	req, _ := http.NewRequest("GET", "https://api.example.com/v1?x=1", nil)
	if err := st.InjectQuery(h.ID, "key", req); err != nil {
		t.Fatal(err)
	}
	if got := req.URL.Query().Get("key"); got != "secret-value-123" {
		t.Fatalf("query: %q", got)
	}
}

func TestInjectBody(t *testing.T) {
	st := newStore(t, []byte("pass"))
	h, _ := st.Add("api-key", []byte("secret-value-123"))

	req, _ := http.NewRequest("POST", "https://api.example.com/v1", nil)
	if err := st.InjectBody(h.ID, req); err != nil {
		t.Fatal(err)
	}
	if req.ContentLength != int64(len("secret-value-123")) {
		t.Fatalf("body length: %d", req.ContentLength)
	}
}

func TestLookupMissing(t *testing.T) {
	st := newStore(t, []byte("pass"))
	if _, _, err := st.Lookup("nonexistent"); err == nil {
		t.Fatal("lookup of nonexistent id should fail")
	}
}

func TestValueNeverInHandle(t *testing.T) {
	st := newStore(t, []byte("pass"))
	h, _ := st.Add("secret-class", []byte("ultra-secret-999"))

	// The handle must not contain the secret value.
	if strings.Contains(h.Class, "ultra-secret-999") || strings.Contains(h.ID, "ultra-secret-999") {
		t.Fatal("handle contains the secret value")
	}

	// scheme.Write of a KHandle must not contain the value either.
	sv := scheme.Value{Kind: scheme.KHandle, H: h}
	if strings.Contains(scheme.Write(sv), "ultra-secret-999") {
		t.Fatal("scheme.Write of handle contains the secret value")
	}
}
