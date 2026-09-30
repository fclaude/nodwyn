package guard

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/fclaude/nodwyn/internal/binding"
	"github.com/fclaude/nodwyn/internal/scheme"
)

func TestEgressConfirmationNamesActualCallScope(t *testing.T) {
	for _, position := range []string{"header:Authorization:Bearer", "query:api_key", "body"} {
		t.Run(position, func(t *testing.T) {
			args := []scheme.Value{
				{Kind: scheme.KHandle, H: &scheme.Handle{Class: "service-key", ID: "opaque-canary"}},
				scheme.Str("https://API.Example.test/v1?ignored=value"), scheme.Str("POST"), scheme.Str(position),
			}
			var prompt Prompt
			calls := 0
			g := New(ConfirmFunc(func(_ context.Context, p Prompt) (bool, error) { prompt = p; calls++; return false, nil }), nil, Egress{Table: binding.Table{}, Policy: "confirm"})
			if err := g.Before(context.Background(), credPrim(), args); err == nil {
				t.Fatal("declined egress allowed")
			}
			want := EgressScope{Class: "service-key", Host: "api.example.test", Method: "POST", Position: position}
			if calls != 1 || prompt.Egress == nil || *prompt.Egress != want {
				t.Fatalf("prompt = %+v", prompt)
			}
			// Default deny still does not ask the user.
			g = New(ConfirmFunc(func(context.Context, Prompt) (bool, error) { t.Fatal("deny prompted"); return true, nil }), nil, Egress{})
			if err := g.Before(context.Background(), credPrim(), args); err == nil {
				t.Fatal("unbound egress allowed")
			}
		})
	}
}

func TestEgressScopeDoesNotInventMissingFacts(t *testing.T) {
	p := &scheme.Prim{Name: "fixed-service", Meta: scheme.Meta{Egress: true, EgressHost: "API.Example.test", Params: []scheme.Param{{Role: "credential"}}}}
	args := []scheme.Value{{Kind: scheme.KHandle, H: &scheme.Handle{Class: "service-key", ID: "opaque"}}}
	got := egressScopeOf(p, args)
	if got == nil || *got != (EgressScope{Class: "service-key", Host: "api.example.test"}) {
		t.Fatalf("scope = %+v", got)
	}
	if egressScopeOf(p, []scheme.Value{scheme.Str("not a handle")}) != nil {
		t.Fatal("non-credential given scope")
	}
	p.Meta.Egress = false
	if egressScopeOf(p, args) != nil {
		t.Fatal("ordinary call given scope")
	}
}

func TestMalformedReloadDeniesEvenWithConfirmPolicy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bindings.toml")
	policy, err := binding.Open(path, binding.Table{})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("unknown = true"), 0o600); err != nil {
		t.Fatal(err)
	}
	g := New(ConfirmFunc(func(context.Context, Prompt) (bool, error) { t.Fatal("invalid policy prompted"); return true, nil }), nil, BindingReload{Policy: policy}, Egress{Table: policy, Policy: "confirm"})
	args := []scheme.Value{{Kind: scheme.KHandle, H: &scheme.Handle{Class: "service"}}, scheme.Str("https://api.test"), scheme.Str("POST"), scheme.Str("body")}
	if g.Before(context.Background(), credPrim(), args) == nil {
		t.Fatal("invalid policy allowed")
	}
}
