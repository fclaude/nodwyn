package guard

import (
	"testing"

	"github.com/fclaude/brote/scheme"
	"github.com/fclaude/nodwyn/internal/binding"
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
	// A method left out or empty is the GET that is sent (methodArg), never
	// the fixed POST; one that is no string is none.
	for _, c := range []struct {
		args   []scheme.Value
		method string
	}{{nil, "GET"}, {[]scheme.Value{scheme.Bool(false), scheme.Bool(false)}, ""}, {[]scheme.Value{scheme.Str(""), scheme.Str("")}, "GET"}} {
		got = egressFactsOf(p, c.args)
		if got.Method != c.method || got.Position != "" {
			t.Fatalf("invalid arguments replaced by fixed facts: %+v", got)
		}
	}
}

// An empty method was judged as "", which every binding row matched whatever
// methods it allows, while the request went out as GET: a POST-only row let
// a GET through. A method is judged as it is sent, upper case and GET when
// empty.
func TestAnEmptyMethodIsJudgedAsTheGETItSends(t *testing.T) {
	p := credPrim()
	for in, want := range map[string]string{"": "GET", "post": "POST", "PUT": "PUT"} {
		args := []scheme.Value{
			{Kind: scheme.KHandle, H: &scheme.Handle{Class: "openai-key", ID: "ab"}},
			scheme.Str("https://api.openai.com/v1/chat"),
			scheme.Str(in),
			scheme.Str("header:Authorization"),
		}
		if got := egressFactsOf(p, args).Method; got != want {
			t.Errorf("method %q is judged as %q, want %q", in, got, want)
		}
	}
	row, err := binding.Exact("openai-key", "api.openai.com", "POST", "header:Authorization")
	if err != nil {
		t.Fatal(err)
	}
	table := binding.Table{Bind: []binding.Binding{row}}
	if bound, _ := table.Lookup("openai-key", "api.openai.com", egressFactsOf(p, []scheme.Value{{Kind: scheme.KHandle, H: &scheme.Handle{Class: "openai-key", ID: "ab"}}, scheme.Str("https://api.openai.com/v1/chat"), scheme.Str(""), scheme.Str("header:Authorization")}).Method, "header:Authorization"); bound {
		t.Fatal("a POST-only row binds an empty method")
	}
}
