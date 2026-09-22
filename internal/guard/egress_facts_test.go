package guard

import (
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
