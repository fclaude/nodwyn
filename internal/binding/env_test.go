package binding

import (
	"context"
	"path/filepath"
	"testing"
)

// A contained MCP server's credential is bound at an env:NAME position
// (ADR 23 W3b): a proposal accepts one, the table looks it up for any
// method, and it binds nothing at a header or in a body.
func TestEnvPositions(t *testing.T) {
	row, err := Exact("notes-key", "api.notes.example", "*", "env:NOTES_TOKEN")
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"env:", "env:1TOKEN", "env:NOTES-TOKEN", "env:A:B", "env:NOTES TOKEN"} {
		if _, err := Exact("notes-key", "api.notes.example", "*", bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	path := filepath.Join(t.TempDir(), "bindings.toml")
	if err := AppendExact(context.Background(), path, row); err != nil {
		t.Fatal(err)
	}
	table, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if bound, reason := table.Lookup("notes-key", "api.notes.example", "", "env:NOTES_TOKEN"); !bound {
		t.Fatal(reason)
	}
	if bound, _ := table.Lookup("notes-key", "api.notes.example", "GET", "header:Authorization"); bound {
		t.Fatal("an env row bound a header")
	}
	if bound, _ := table.Lookup("notes-key", "other.example", "", "env:NOTES_TOKEN"); bound {
		t.Fatal("an env row bound another host")
	}
	if where, name, _ := Position("env:NOTES_TOKEN"); where != "env" || name != "NOTES_TOKEN" {
		t.Fatalf("%s %s", where, name)
	}
}
