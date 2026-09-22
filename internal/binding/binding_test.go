package binding

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLookupBound(t *testing.T) {
	tbl := Table{Bind: []Binding{
		{Class: "openai-key", Dest: "api.openai.com", Positions: []string{"header:Authorization"}, Methods: []string{"POST"}},
	}}
	bound, reason := tbl.Lookup("openai-key", "api.openai.com", "POST", "header:Authorization")
	if !bound {
		t.Fatalf("expected bound, got: %s", reason)
	}
}

func TestLookupUnbound(t *testing.T) {
	tbl := Table{Bind: []Binding{
		{Class: "openai-key", Dest: "api.openai.com", Positions: []string{"header:Authorization"}, Methods: []string{"POST"}},
	}}

	// Wrong host.
	if bound, reason := tbl.Lookup("openai-key", "evil.com", "POST", "header:Authorization"); bound || !strings.Contains(reason, "not bound") {
		t.Fatalf("wrong host: bound=%v reason=%q", bound, reason)
	}

	// Wrong method.
	if bound, reason := tbl.Lookup("openai-key", "api.openai.com", "DELETE", "header:Authorization"); bound || !strings.Contains(reason, "method") {
		t.Fatalf("wrong method: bound=%v reason=%q", bound, reason)
	}

	// Wrong position.
	if bound, reason := tbl.Lookup("openai-key", "api.openai.com", "POST", "body"); bound || !strings.Contains(reason, "position") {
		t.Fatalf("wrong position: bound=%v reason=%q", bound, reason)
	}

	// Unknown class.
	if bound, reason := tbl.Lookup("jev-key", "api.typesafe.ai", "POST", "header:Authorization"); bound || !strings.Contains(reason, "no binding") {
		t.Fatalf("unknown class: bound=%v reason=%q", bound, reason)
	}
}

func TestWildcardHost(t *testing.T) {
	tbl := Table{Bind: []Binding{
		{Class: "key", Dest: "*.example.com", Positions: []string{"header:X"}, Methods: []string{"GET"}},
	}}
	if bound, _ := tbl.Lookup("key", "api.example.com", "GET", "header:X"); !bound {
		t.Fatal("wildcard should match subdomain")
	}
	if bound, _ := tbl.Lookup("key", "evil.com", "GET", "header:X"); bound {
		t.Fatal("wildcard should not match unrelated host")
	}
}

func TestHasDestination(t *testing.T) {
	tbl := Table{Bind: []Binding{
		{Class: "a", Dest: "first.example"},
		{Class: "b", Dest: "*.example.com"},
	}}
	for _, tc := range []struct {
		host string
		want bool
	}{
		{"first.example", true}, {"api.example.com", true},
		{"example.com", false}, {"notexample.com", false},
		{"elsewhere.example", false}, {"", false},
	} {
		if got := tbl.HasDestination(tc.host); got != tc.want {
			t.Errorf("HasDestination(%q) = %v, want %v", tc.host, got, tc.want)
		}
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bindings.toml")
	if _, err := Load(p); err != nil {
		t.Fatalf("missing file: %v", err)
	}
	os.WriteFile(p, []byte(`
[[bind]]
class = "openai-key"
dest = "api.openai.com"
positions = ["header:Authorization"]
methods = ["POST"]
`), 0o600)
	tbl, err := Load(p)
	if err != nil || len(tbl.Bind) != 1 || tbl.Bind[0].Class != "openai-key" {
		t.Fatalf("load: %v %+v", err, tbl)
	}
}

func TestLoadIncomplete(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bindings.toml")
	os.WriteFile(p, []byte(`
[[bind]]
class = "x"
`), 0o600)
	if _, err := Load(p); err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("incomplete row accepted: %v", err)
	}
}

func TestLookupConsidersEveryRowOfAClass(t *testing.T) {
	tb := Table{Bind: []Binding{
		{Class: "k", Dest: "a.example", Positions: []string{"header:Authorization"}, Methods: []string{"POST"}},
		{Class: "k", Dest: "b.example", Positions: []string{"query:key"}, Methods: []string{"GET"}},
	}}
	if ok, _ := tb.Lookup("k", "b.example", "GET", "query:key"); !ok {
		t.Fatal("second row for the class should bind")
	}
	if ok, reason := tb.Lookup("k", "b.example", "POST", "query:key"); ok || reason == "" {
		t.Fatalf("method from the other row must not leak across rows: %v %q", ok, reason)
	}
	if ok, reason := tb.Lookup("k", "c.example", "GET", "query:key"); ok || !strings.Contains(reason, "not bound") {
		t.Fatalf("unbound host: %v %q", ok, reason)
	}
}

func TestPositionParsesScheme(t *testing.T) {
	for pos, want := range map[string][3]string{
		"header:Authorization:Bearer": {"header", "Authorization", "Bearer "},
		"header:x-api-key":            {"header", "x-api-key", ""},
		"query:key":                   {"query", "key", ""},
		"body":                        {"body", "", ""},
	} {
		w, n, p := Position(pos)
		if w != want[0] || n != want[1] || p != want[2] {
			t.Errorf("Position(%q) = %q %q %q, want %v", pos, w, n, p, want)
		}
	}
}
