package rpc

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestWireSchemaMatchesContract(t *testing.T) {
	got, err := WireSchema()
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("../../../docs/protocol.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bytes.TrimSpace(got), bytes.TrimSpace(want)) {
		t.Fatal("wire schema is stale: run go generate ./internal/frontend/rpc")
	}
	var doc map[string]any
	if err := json.Unmarshal(got, &doc); err != nil {
		t.Fatal(err)
	}
	defs := doc["$defs"].(map[string]any)
	if len(contracts()) != len(methods)+1 {
		t.Fatal("schema and hello method sets differ")
	}
	for _, method := range methods {
		if _, ok := defs["request."+method]; !ok {
			t.Fatal("missing method schema", method)
		}
	}
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if ref, ok := x["$ref"].(string); ok {
				if _, ok := defs[strings.TrimPrefix(ref, "#/$defs/")]; !ok {
					t.Error("unresolved reference", ref)
				}
			}
			for _, child := range x {
				walk(child)
			}
		case []any:
			for _, child := range x {
				walk(child)
			}
		}
	}
	walk(doc)
}
