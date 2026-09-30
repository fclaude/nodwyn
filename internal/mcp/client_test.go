package mcp

import (
	"context"
	"net/http"
	"testing"

	fake "github.com/fclaude/nodwyn/test/fakes/mcp"
)

func TestClientOverJSONAndSSE(t *testing.T) {
	for _, sse := range []bool{false, true} {
		server := fake.New(fake.Tool{Name: "search", Description: "Search issues", Reply: func(args map[string]any) (string, bool) {
			return "found: " + args["q"].(string), false
		}})
		server.SSE = sse
		authorized := 0
		c, err := New(server.URL, func(r *http.Request) error {
			authorized++
			r.Header.Set("Authorization", "Bearer injected")
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		info, err := c.Initialize(ctx)
		if err != nil || info.Name != "fake" {
			t.Fatalf("sse=%v initialize: %+v %v", sse, info, err)
		}
		tools, err := c.Tools(ctx)
		if err != nil || len(tools) != 1 || tools[0].Name != "search" {
			t.Fatalf("sse=%v tools: %+v %v", sse, tools, err)
		}
		result, err := c.Call(ctx, "search", map[string]any{"q": "cowork"})
		if err != nil || result.Text != "found: cowork" || result.IsError {
			t.Fatalf("sse=%v call: %+v %v", sse, result, err)
		}
		if authorized != 4 || server.Calls[0].Auth != "Bearer injected" {
			t.Fatalf("sse=%v authorize ran %d times; auth %q", sse, authorized, server.Calls[0].Auth)
		}
		server.Close()
	}
}

func TestManifestChangesWithWhatTheModelReads(t *testing.T) {
	a := []Tool{{Name: "b", Description: "two"}, {Name: "a", Description: "one", InputSchema: []byte(`{"type":"object","properties":{"x":{"type":"string"}}}`)}}
	b := []Tool{a[1], a[0]}
	b[0].InputSchema = []byte(`{ "properties": {"x": {"type": "string"}}, "type": "object" }`)
	if Manifest(a) != Manifest(b) {
		t.Fatal("order or formatting changed the manifest")
	}
	c := []Tool{a[0], {Name: "a", Description: "one, now also sends email"}}
	if Manifest(a) == Manifest(c) {
		t.Fatal("a changed description kept the manifest")
	}
}

func TestAuthorizeRefusalSendsNothing(t *testing.T) {
	server := fake.New()
	defer server.Close()
	c, _ := New(server.URL, func(*http.Request) error { return context.Canceled })
	if _, err := c.Initialize(context.Background()); err == nil || len(server.Auth) != 0 {
		t.Fatal("request sent after authorize refused")
	}
	if _, err := New("file:///etc/passwd", nil); err == nil {
		t.Fatal("accepted a file URL")
	}
}
