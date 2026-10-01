package guard

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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
			if calls != 1 || prompt.Egress == nil || !reflect.DeepEqual(*prompt.Egress, want) {
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
	got := egressScopeOf(p, args, nil)
	if got == nil || !reflect.DeepEqual(*got, EgressScope{Class: "service-key", Host: "api.example.test"}) {
		t.Fatalf("scope = %+v", got)
	}
	if egressScopeOf(p, []scheme.Value{scheme.Str("not a handle")}, nil) != nil {
		t.Fatal("non-credential given scope")
	}
	p.Meta.Egress = false
	if egressScopeOf(p, args, nil) != nil {
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

// An OAuth2 credential's confirm names its scopes, its proposed binding row
// pins them, and a row naming scopes refuses a token granted more.
func TestEgressNamesAndChecksScopes(t *testing.T) {
	scopes := func(class, id string) []string {
		if class == "docs" && id == "h1" {
			return []string{"drive.file", "drive"}
		}
		return nil
	}
	args := []scheme.Value{
		{Kind: scheme.KHandle, H: &scheme.Handle{Class: "docs", ID: "h1"}},
		scheme.Str("https://docs.example.test/v1"), scheme.Str("POST"), scheme.Str("header:Authorization:Bearer"),
	}
	var prompt Prompt
	e := Egress{Table: binding.Table{}, Policy: "confirm", Scopes: scopes}
	g := New(ConfirmFunc(func(_ context.Context, p Prompt) (bool, error) { prompt = p; return false, nil }), nil, e).WithCredentialScopes(scopes)
	if err := g.Before(context.Background(), credPrim(), args); err == nil {
		t.Fatal("declined egress allowed")
	}
	if prompt.Egress == nil || strings.Join(prompt.Egress.Scopes, " ") != "drive.file drive" {
		t.Fatalf("the confirm does not name the scopes: %+v", prompt.Egress)
	}
	if prompt.Binding == nil || strings.Join(prompt.Binding.Scopes, " ") != "drive.file drive" {
		t.Fatalf("the proposed row does not pin the scopes: %+v", prompt.Binding)
	}
	row := binding.Binding{Class: "docs", Dest: "docs.example.test", Methods: []string{"POST"}, Positions: []string{"header:Authorization:Bearer"}, Scopes: []string{"drive.file"}}
	d := Egress{Table: binding.Table{Bind: []binding.Binding{row}}, Scopes: scopes}.Check(context.Background(), credPrim(), args)
	if d.Verdict != Deny || !strings.Contains(d.Reason, "scope drive of the docs credential") {
		t.Fatalf("a token granted more than the row allows: %+v", d)
	}
	row.Scopes = append(row.Scopes, "drive")
	if d := (Egress{Table: binding.Table{Bind: []binding.Binding{row}}, Scopes: scopes}).Check(context.Background(), credPrim(), args); d.Verdict != Allow {
		t.Fatalf("every granted scope allowed: %+v", d)
	}
}
