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

// Optional parameters come last, are left out of the schema's required list,
// and a call may stop before any of them: the handler gets fewer arguments,
// never placeholders. One given after a missing optional one is an error,
// since positions would shift.
func TestOptionalParameters(t *testing.T) {
	in := New()
	var got []Value
	p := &Prim{Name: "ranged", Meta: Meta{Doc: "d", Host: true, Params: []Param{
		{Name: "path", Type: "string"},
		{Name: "start", Type: "integer", Optional: true},
		{Name: "count", Type: "integer", Optional: true},
	}}, Handler: func(c *Call) (Value, error) { got = c.Args; return Void, nil }}
	if err := in.Register(p); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(p.Schema())
	if !strings.Contains(string(b), `"required":["path"]`) {
		t.Fatalf("schema: %s", b)
	}
	for raw, n := range map[string]int{
		`{"path":"p"}`:                           1,
		`{"path":"p","start":3}`:                 2,
		`{"path":"p","start":3,"count":4}`:       3,
		`{"path":"p","start":null,"count":null}`: 1,
		`{"path":"p","start":3,"count":null}`:    2,
	} {
		args, err := p.ArgsFromJSON([]byte(raw))
		if err != nil || len(args) != n {
			t.Errorf("%s: %d args, %v; want %d", raw, len(args), err, n)
		}
	}
	for raw, want := range map[string]string{
		`{"path":"p","count":4}`:              `argument "count" needs "start" too`,
		`{"path":"p","start":null,"count":4}`: `argument "count" needs "start" too`,
		`{"path":"p","start":"3"}`:            `argument "start": type: expected integer`,
		`{"start":3}`:                         `missing argument "path"`,
	} {
		if _, err := p.ArgsFromJSON([]byte(raw)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v, want %q", raw, err, want)
		}
	}
	for src, n := range map[string]int{`(ranged "p")`: 1, `(ranged "p" 3)`: 2, `(ranged "p" 3 4)`: 3} {
		if _, err := in.EvalString(t.Context(), src); err != nil || len(got) != n {
			t.Errorf("%s: %d args, %v", src, len(got), err)
		}
	}
	for _, src := range []string{`(ranged)`, `(ranged "p" 3 4 5)`} {
		if _, err := in.EvalString(t.Context(), src); err == nil || !strings.Contains(err.Error(), "expected 1 to 3 argument(s)") {
			t.Errorf("%s: %v", src, err)
		}
	}
	h := func(c *Call) (Value, error) { return Void, nil }
	bad := []*Prim{
		{Name: "gap", Meta: Meta{Doc: "d", Params: []Param{{Name: "a", Optional: true}, {Name: "b"}}}, Handler: h},
		{Name: "both", Meta: Meta{Doc: "d", Variadic: true, Params: []Param{{Name: "a", Optional: true}}}, Handler: h},
	}
	for _, p := range bad {
		if err := in.Register(p); err == nil {
			t.Errorf("%s registered", p.Name)
		}
	}
}
