package binding

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func exact(t *testing.T, host string) Binding {
	t.Helper()
	row, err := Exact("service", host, "POST", "header:Authorization:Bearer")
	if err != nil {
		t.Fatal(err)
	}
	return row
}
func TestAppendPreservesPolicyAndConcurrentRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bindings.toml")
	original := "# my policy\n[[bind]]\nclass = 'other'\ndest = '*.old.test'\npositions = ['body']\nmethods = ['PUT'] # intentional\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for _, host := range []string{"one.test", "two.test", "one.test"} {
		row := exact(t, host)
		wg.Go(func() {
			if err := AppendExact(context.Background(), path, row); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	table, err := Load(path)
	if err != nil || len(table.Bind) != 3 {
		t.Fatal(table, err)
	}
	raw, err := os.ReadFile(path)
	if err != nil || !strings.HasPrefix(string(raw), original) {
		t.Fatal("user bytes changed", err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 {
		t.Fatal("policy permissions", info.Mode())
	}
	for _, host := range []string{"one.test", "two.test"} {
		if bound, _ := table.Lookup("service", host, "POST", "header:Authorization:Bearer"); !bound {
			t.Fatal("lost concurrent row", host)
		}
	}
}
func TestAppendRejectsInvalidFileAndCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bindings.toml")
	row := exact(t, "api.test")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if !errors.Is(AppendExact(ctx, path, row), context.Canceled) {
		t.Fatal("canceled append")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("canceled append persisted")
	}
	for _, raw := range []string{"[broken", "unknown = true", "[[bind]]\nclass='incomplete'"} {
		if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := AppendExact(context.Background(), path, row); err == nil {
			t.Fatal("accepted invalid policy")
		}
		got, _ := os.ReadFile(path)
		if string(got) != raw {
			t.Fatal("invalid policy overwritten")
		}
	}
	target := filepath.Join(t.TempDir(), "target")
	os.WriteFile(target, []byte("# target"), 0o600)
	os.Remove(path)
	os.Symlink(target, path)
	if AppendExact(context.Background(), path, row) == nil {
		t.Fatal("replaced symlink")
	}
}
func TestLiveReloadRemovalFailureAndIsolation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bindings.toml")
	initial := Table{Bind: []Binding{exact(t, "initial.test")}}
	memory, err := Open("", initial)
	if err != nil {
		t.Fatal(err)
	}
	live, err := Open(path, initial)
	if err != nil {
		t.Fatal(err)
	}
	initial.Bind[0].Dest = "changed.test"
	if bound, _ := memory.Lookup("service", "initial.test", "POST", "header:Authorization:Bearer"); !bound {
		t.Fatal("initial table aliased")
	}
	if len(live.Snapshot().Bind) != 0 {
		t.Fatal("missing file restored stale startup authority")
	}
	if err := AppendExact(context.Background(), path, exact(t, "new.test")); err != nil {
		t.Fatal(err)
	}
	if err := live.Refresh(); err != nil {
		t.Fatal(err)
	}
	snap := live.Snapshot()
	snap.Bind[0].Methods[0] = "DELETE"
	if bound, _ := live.Lookup("service", "new.test", "POST", "header:Authorization:Bearer"); !bound {
		t.Fatal("snapshot aliased")
	}
	os.WriteFile(path, []byte("invalid = true"), 0o600)
	for range 2 {
		if live.Refresh() == nil {
			t.Fatal("invalid file silently accepted")
		}
	}
	os.Remove(path)
	if err := live.Refresh(); err != nil || len(live.Snapshot().Bind) != 0 {
		t.Fatal("removed policy retained", err)
	}
}
func TestExactScopeRejectsBroadOrMissingFacts(t *testing.T) {
	for _, args := range [][4]string{
		{"", "api.test", "POST", "body"}, {"service", "*.test", "POST", "body"},
		{"service", "https://api.test", "POST", "body"}, {"service", "api.test", "", "body"},
		{"service", "api.test", "POST", ""}, {"service", "api.test", "POST", "header:"},
		{"service", "api.test", "POST", "query:"}, {"service", "api.test", "POST", "body:extra"},
		{"service", "api.test", "POST\nGET", "body"},
	} {
		if _, err := Exact(args[0], args[1], args[2], args[3]); err == nil {
			t.Fatal("accepted", args)
		}
	}
}
