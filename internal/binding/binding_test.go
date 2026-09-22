package binding

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fclaude/nibb/internal/scheme"
)

// credPrim builds a test prim with credential + dest + method + position params.
func credPrim() *scheme.Prim {
	return &scheme.Prim{
		Name: "http-request",
		Meta: scheme.Meta{
			Egress: true,
			Params: []scheme.Param{
				{Name: "cred", Type: "any", Role: "credential"},
				{Name: "url", Type: "string", Role: "dest"},
				{Name: "method", Type: "string", Role: "method"},
				{Name: "position", Type: "string", Role: "position"},
			},
		},
	}
}

func TestFactsOf(t *testing.T) {
	p := credPrim()
	args := []scheme.Value{
		scheme.Value{Kind: scheme.KHandle, H: &scheme.Handle{Class: "openai-key", ID: "ab"}},
		scheme.Str("https://api.openai.com/v1/chat"),
		scheme.Str("POST"),
		scheme.Str("header:Authorization"),
	}
	f := Table{}.FactsOf(p, args)
	if !f.HasCredential {
		t.Fatal("expected HasCredential")
	}
	if f.Class != "openai-key" {
		t.Fatalf("class: %q", f.Class)
	}
	if f.Host != "api.openai.com" {
		t.Fatalf("host: %q", f.Host)
	}
	if f.Method != "POST" {
		t.Fatalf("method: %q", f.Method)
	}
	if f.Position != "header:Authorization" {
		t.Fatalf("position: %q", f.Position)
	}
}

func TestFactsOfNoCred(t *testing.T) {
	p := credPrim()
	// No handle in arg 0.
	args := []scheme.Value{
		scheme.Str("not-a-handle"),
		scheme.Str("https://api.openai.com/v1/chat"),
		scheme.Str("POST"),
		scheme.Str("header:Authorization"),
	}
	f := Table{}.FactsOf(p, args)
	if f.HasCredential {
		t.Fatal("expected no credential")
	}
}

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
