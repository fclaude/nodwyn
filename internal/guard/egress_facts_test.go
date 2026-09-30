package guard

import (
	"testing"

	"github.com/fclaude/nodwyn/internal/scheme"
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
	f := egressFactsOf(p, args)
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
	f := egressFactsOf(p, args)
	if f.HasCredential {
		t.Fatal("expected no credential")
	}
}

func TestFixedEgressScopeAndArgumentPrecedence(t *testing.T) {
	p := &scheme.Prim{Meta: scheme.Meta{Egress: true, EgressHost: "api.example.test", EgressMethod: "POST", EgressPosition: "header:Authorization:Bearer"}}
	got := egressFactsOf(p, nil)
	if got.Method != "POST" || got.Position != "header:Authorization:Bearer" {
		t.Fatalf("fixed facts: %+v", got)
	}
	p.Meta.Params = []scheme.Param{{Role: "method"}, {Role: "position"}}
	got = egressFactsOf(p, []scheme.Value{scheme.Str("GET"), scheme.Str("query:key")})
	if got.Method != "GET" || got.Position != "query:key" {
		t.Fatalf("arguments ignored: %+v", got)
	}
	for _, args := range [][]scheme.Value{nil, {scheme.Bool(false), scheme.Bool(false)}, {scheme.Str(""), scheme.Str("")}} {
		got = egressFactsOf(p, args)
		if got.Method != "" || got.Position != "" {
			t.Fatalf("invalid arguments replaced by fixed facts: %+v", got)
		}
	}
}
