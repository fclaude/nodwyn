package keychain

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A refreshed token set whose save failed stays in effect across the next
// reload: the provider already retired the refresh token the file holds, so
// reading the file back would burn the grant (P2-007).
func TestOAuth2UnsavedRefreshSurvivesTheNextReload(t *testing.T) {
	st, srv, c := oauthStore(t)
	srv.SetRotate(true)
	h, err := st.AddOAuth2(context.Background(), "docs", grant(srv), srv.Browser)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(st.path)
	c.Add(time.Hour - 30*time.Second)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	err = st.Refresh(context.Background(), h.ID, nil)
	if chmodErr := os.Chmod(dir, 0o700); chmodErr != nil {
		t.Fatal(chmodErr)
	}
	if err == nil {
		t.Skip("the directory stayed writable (running as root?)")
	}
	c.Add(time.Hour)
	if err := st.Refresh(context.Background(), h.ID, nil); err != nil {
		t.Fatalf("second refresh after a failed save: %v", err)
	}
	if info, _ := st.Info(h.ID); info.NeedsReauth {
		t.Fatal("the retired refresh token was sent again")
	}
	// The second refresh saved: the file holds the newest set now, so
	// another process refreshes from it without trouble.
	other, err := Open(st.path, []byte("pass"))
	if err != nil {
		t.Fatal(err)
	}
	c.Add(time.Hour)
	other.SetClock(c.Now)
	other.SetHTTPClient(st.client)
	if err := other.Refresh(context.Background(), h.ID, nil); err != nil {
		t.Fatalf("a second process refreshing from the file: %v", err)
	}
}

// One class names one credential: a second would never be sent, since a
// lookup by class finds the first (P2-006).
func TestAddRefusesABadOrTakenClass(t *testing.T) {
	st := newStore(t, []byte("pass"))
	if _, err := st.Add("github", []byte("one")); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Add("github", []byte("two")); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("a second credential of one class: %v", err)
	}
	for _, class := range []string{"", "bad class!", "-leading"} {
		if _, err := st.Add(class, []byte("x")); err == nil {
			t.Fatalf("class %q was accepted", class)
		}
	}
	if st.Count() != 1 {
		t.Fatalf("count %d, want 1", st.Count())
	}
}

// A passphrase changed by another process leaves this store stale: it says
// so, and Reopen with the new passphrase makes it work again (P2-003).
func TestReopenAfterThePassphraseChangedElsewhere(t *testing.T) {
	st := newStore(t, []byte("old pass"))
	if _, err := st.Add("github", []byte("one")); err != nil {
		t.Fatal(err)
	}
	if st.Stale() {
		t.Fatal("a store nobody changed is stale")
	}
	other, err := Open(st.path, []byte("old pass"))
	if err != nil {
		t.Fatal(err)
	}
	if err := other.SetPassphrase([]byte("new pass")); err != nil {
		t.Fatal(err)
	}
	if !st.Stale() {
		t.Fatal("a store whose passphrase changed elsewhere is not stale")
	}
	if _, err := st.Add("other", []byte("two")); err == nil {
		t.Fatal("a stale store changed the keychain")
	}
	if err := st.Reopen([]byte("old pass")); err == nil {
		t.Fatal("the old passphrase reopened it")
	}
	if err := st.Reopen([]byte("new pass")); err != nil {
		t.Fatal(err)
	}
	if st.Stale() {
		t.Fatal("still stale after Reopen")
	}
	if _, err := st.Add("other", []byte("two")); err != nil {
		t.Fatalf("add after Reopen: %v", err)
	}
	if st.Count() != 2 {
		t.Fatalf("count %d, want 2", st.Count())
	}
}
