package catalogue

import (
	"encoding/json"
	"reflect"
	"testing"
)

// models.dev's shape, cut down: a provider nodwyn knows with a reasoning
// model, a text model that toggles its thinking, an image model, and a
// provider nodwyn does not know.
const modelsDev = `{
  "openai": {"id": "openai", "models": {
    "sol": {"limit": {"context": 1050000, "input": 922000, "output": 128000},
      "reasoning_options": [{"type": "effort", "values": ["low", "medium", "high", "xhigh", "max"]}],
      "modalities": {"input": ["text", "image"], "output": ["text"]},
      "cost": {"input": 2, "output": 10, "cache_read": 0.1, "cache_write": 2.5,
        "tiers": [{"input": 4, "output": 15, "cache_read": 0.2, "tier": {"type": "context", "size": 272000}},
                  {"input": 9, "output": 9, "tier": {"type": "requests", "size": 5}}]}},
    "toggler": {"limit": {"context": 131072, "output": 40960},
      "reasoning_options": [{"type": "toggle"}],
      "modalities": {"input": ["text"], "output": ["text"]}},
    "painter": {"limit": {"context": 4096, "output": 4096}, "modalities": {"input": ["text"], "output": ["image"]}}
  }},
  "elsewhere": {"id": "elsewhere", "models": {"x": {"limit": {"context": 8192}, "modalities": {"output": ["text"]}}}}
}`

// The snapshot keeps what a session uses of each model that writes text, at
// a provider nodwyn knows, and nothing else: no toggle as an effort, no tier
// that is not the context's, no provider without an address in Providers.
func TestFromModelsDev(t *testing.T) {
	d, err := FromModelsDev([]byte(modelsDev), "2026-10-05")
	if err != nil {
		t.Fatal(err)
	}
	read, write := 0.1, 2.5
	tierRead := 0.2
	want := map[string]map[string]Model{"openai": {
		"sol": {Context: 1050000, Output: 128000, Efforts: []string{"low", "medium", "high", "xhigh", "max"}, Images: true,
			Price: &Price{Input: 2, Output: 10, CacheRead: &read, CacheWrite: &write, Tiers: []Tier{{Above: 272000, Input: 4, Output: 15, CacheRead: &tierRead}}}},
		"toggler": {Context: 131072, Output: 40960},
	}}
	if !reflect.DeepEqual(d.Providers, want) || d.Source != Source || d.Fetched != "2026-10-05" {
		got, _ := json.Marshal(d)
		t.Fatalf("snapshot:\n%s", got)
	}
	// What Encode writes reads back as the same snapshot.
	text, err := Encode(d)
	if err != nil {
		t.Fatal(err)
	}
	var back Data
	if err := json.Unmarshal(text, &back); err != nil || !reflect.DeepEqual(back, d) {
		t.Fatalf("encoded:\n%s\n%v", text, err)
	}
}

// A model is found by the host of its server's address, whatever the path,
// the case or a trailing slash; another host, or a model the provider does
// not publish, is not described.
func TestLookupByHost(t *testing.T) {
	c := New(map[string]map[string]Model{"api.openai.com": {"sol": {Context: 1050000}}})
	for _, base := range []string{"https://api.openai.com/v1", "https://API.openai.com/v1/", "https://api.openai.com"} {
		if m, ok := c.Lookup(base, "sol"); !ok || m.Context != 1050000 {
			t.Errorf("%s: %+v, %v", base, m, ok)
		}
	}
	for _, tc := range [][2]string{{"https://api.openai.com/v1", "luna"}, {"https://openai.example.com/v1", "sol"}, {"::", "sol"}} {
		if _, ok := c.Lookup(tc[0], tc[1]); ok {
			t.Errorf("%s %s was described", tc[0], tc[1])
		}
	}
	var none *Catalogue
	if _, ok := none.Lookup("https://api.openai.com/v1", "sol"); ok {
		t.Error("a nil catalogue described a model")
	}
}

// The snapshot built in reads, and describes models at each provider's
// address.
func TestTheBuiltInSnapshot(t *testing.T) {
	var d Data
	if err := json.Unmarshal(embedded, &d); err != nil {
		t.Fatal(err)
	}
	if d.Source != Source || d.Fetched == "" || len(d.Providers["openai"]) == 0 {
		t.Fatalf("snapshot from %q on %q with %d OpenAI models", d.Source, d.Fetched, len(d.Providers["openai"]))
	}
	for id, m := range d.Providers["openai"] {
		if got, ok := Default().Lookup("https://api.openai.com/v1", id); !ok || !reflect.DeepEqual(got, m) {
			t.Fatalf("%s is not found at OpenAI's address", id)
		}
		break
	}
}
