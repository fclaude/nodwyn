package keychain

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fclaude/brote/scheme"
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
	info, err := st2.Info(h.ID)
	if err != nil || info.Class != "openai-key" {
		t.Fatalf("info: %q %v", info.Class, err)
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

func TestInfoMissing(t *testing.T) {
	st := newStore(t, []byte("pass"))
	if _, err := st.Info("nonexistent"); err == nil {
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

// A keychain without a passphrase is sealed under the empty one: it opens
// without asking, and one that has a passphrase says so instead of failing
// as a wrong guess would.
func TestPasswordless(t *testing.T) {
	st := newStore(t, nil)
	if st.HasPassphrase() {
		t.Fatal("created without a passphrase, but has one")
	}
	h, err := st.Add("api-key", []byte("secret-value-123"))
	if err != nil {
		t.Fatal(err)
	}
	again, err := OpenWithoutPassphrase(st.path)
	if err != nil {
		t.Fatal(err)
	}
	if again.HasPassphrase() || again.Count() != 1 {
		t.Fatalf("reopened: passphrase %v, %d credentials", again.HasPassphrase(), again.Count())
	}
	req, _ := http.NewRequest("GET", "https://api.example.com/v1", nil)
	if err := again.InjectHeader(h.ID, "Authorization", "Bearer ", req); err != nil || req.Header.Get("Authorization") != "Bearer secret-value-123" {
		t.Fatalf("inject: %v %q", err, req.Header.Get("Authorization"))
	}

	locked := newStore(t, []byte("pass"))
	if !locked.HasPassphrase() {
		t.Fatal("created with a passphrase, but has none")
	}
	if _, err := OpenWithoutPassphrase(locked.path); !errors.Is(err, ErrNeedsPassphrase) {
		t.Fatalf("keychain with a passphrase: %v", err)
	}
	if _, err := OpenWithoutPassphrase(t.TempDir() + "/none"); err == nil || errors.Is(err, ErrNeedsPassphrase) {
		t.Fatalf("missing keychain: %v", err)
	}
}

// The passphrase can be set, changed and removed; each time the file opens
// with the new one alone, and keeps its credentials.
func TestSetPassphrase(t *testing.T) {
	st := newStore(t, nil)
	h, _ := st.Add("api-key", []byte("secret-value-123"))
	path := st.path

	if err := st.SetPassphrase([]byte("first")); err != nil {
		t.Fatal(err)
	}
	if !st.HasPassphrase() {
		t.Fatal("set, but has none")
	}
	if _, err := OpenWithoutPassphrase(path); !errors.Is(err, ErrNeedsPassphrase) {
		t.Fatalf("after setting one: %v", err)
	}
	other, err := Open(path, []byte("first"))
	if err != nil || other.Count() != 1 {
		t.Fatalf("open with the new passphrase: %v", err)
	}

	if err := st.SetPassphrase([]byte("second")); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path, []byte("first")); err == nil {
		t.Fatal("the old passphrase still opens it")
	}
	// The process that held it under the old passphrase can no longer
	// change it, and says to unlock again.
	if _, err := other.Add("more", []byte("x")); err == nil || !strings.Contains(err.Error(), "unlock it again") {
		t.Fatalf("change under the old passphrase: %v", err)
	}
	// The one that changed it carries on.
	if _, err := st.Add("more", []byte("x")); err != nil {
		t.Fatal(err)
	}

	if err := st.SetPassphrase(nil); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenWithoutPassphrase(path)
	if err != nil || reopened.Count() != 2 || reopened.HasPassphrase() {
		t.Fatalf("after removing it: %v", err)
	}
	if info, err := reopened.Info(h.ID); err != nil || info.Class != "api-key" {
		t.Fatalf("info: %q %v", info.Class, err)
	}
}

// Replacing a static key keeps its id and class, so whatever names the
// class sends the new value.
func TestReplace(t *testing.T) {
	st := newStore(t, []byte("pass"))
	h, _ := st.Add("api-key", []byte("old-value"))
	if err := st.Replace(h.ID, []byte("new-value")); err != nil {
		t.Fatal(err)
	}
	reopened, _ := Open(st.path, []byte("pass"))
	req, _ := http.NewRequest("GET", "https://api.example.com/v1", nil)
	if err := reopened.InjectHeader(h.ID, "x-api-key", "", req); err != nil || req.Header.Get("x-api-key") != "new-value" {
		t.Fatalf("after replace: %v %q", err, req.Header.Get("x-api-key"))
	}
	if got := reopened.HandleForClass("api-key"); got == nil || got.ID != h.ID {
		t.Fatalf("handle for class: %+v", got)
	}
	if err := st.Replace("nonexistent", []byte("x")); err == nil {
		t.Fatal("replaced a credential that does not exist")
	}
}

// Create never replaces a keychain: two creates at once, or a caller that
// did not look first, would replace every key in it.
func TestCreateRefusesAnExistingKeychain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keychain")
	st, err := Create(path, FastKDF, []byte("first"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Add("svc", []byte("kept-value")); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(path, FastKDF, []byte("second")); !errors.Is(err, os.ErrExist) {
		t.Fatalf("a second create: %v", err)
	}
	again, err := Open(path, []byte("first"))
	if err != nil || again.HandleForClass("svc") == nil {
		t.Fatalf("the first keychain is gone: %v", err)
	}
}
