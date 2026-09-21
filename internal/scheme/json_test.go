package scheme

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSchemaAndArgsFromJSON(t *testing.T) {
	p := &Prim{Name: "t", Meta: Meta{Doc: "d", Params: []Param{
		{Name: "path", Type: "string", Doc: "a path"},
		{Name: "n", Type: "integer"},
		{Name: "flags", Type: "list"},
		{Name: "opts", Type: "alist"},
		{Name: "x", Type: "any"},
	}}}
	b, _ := json.Marshal(p.Schema())
	want := `{"type":"object","properties":{"flags":{"type":"array"},"n":{"type":"integer"},"opts":{"type":"object"},"path":{"type":"string","description":"a path"},"x":{}},"required":["path","n","flags","opts","x"]}`
	if string(b) != want {
		t.Fatalf("schema:\n%s\n%s", b, want)
	}
	args, err := p.ArgsFromJSON([]byte(`{"x":null,"opts":{"k":1},"flags":["a"],"n":2,"path":"p"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := Write(List(args...)); got != `("p" 2 ("a") (("k" . 1)) null)` {
		t.Fatalf("args: %s", got)
	}
	for raw, want := range map[string]string{
		`{"path":"p","n":2,"flags":[],"opts":[]}`:              `missing argument "x"`,
		`{"path":1,"n":2,"flags":[],"opts":[],"x":0}`:          `argument "path": type: expected string`,
		`{"path":"p","n":2.5,"flags":[],"opts":[],"x":0}`:      `expected integer`,
		`{"path":"p","n":2,"flags":[],"opts":[],"x":0,"zz":1}`: `unknown argument(s): zz`,
		`[1]`: `must be a JSON object`,
	} {
		_, err := p.ArgsFromJSON([]byte(raw))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v, want %q", raw, err, want)
		}
	}
	empty := &Prim{Name: "e", Meta: Meta{Doc: "d"}}
	if args, err := empty.ArgsFromJSON(nil); err != nil || len(args) != 0 {
		t.Fatalf("empty: %v %v", args, err)
	}
}

func TestToolsExcludeBuiltinsAndHidden(t *testing.T) {
	in := New()
	for _, p := range in.Tools() {
		if p.Name == "load" || p.Name == "car" {
			t.Fatalf("%s should not be a tool", p.Name)
		}
	}
	if err := in.Register(&Prim{Name: "host-thing", Meta: Meta{Doc: "d", Host: true}, Handler: func(c *Call) (Value, error) { return Void, nil }}); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range in.Tools() {
		found = found || p.Name == "host-thing"
	}
	if !found {
		t.Fatal("host prim should be a tool")
	}
}
