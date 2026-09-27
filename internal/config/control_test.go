package config

import (
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// The table MissingControl suggests must be one nibb accepts as written.
func TestMissingControlExampleIsValid(t *testing.T) {
	msg := MissingControl("").Error()
	table := msg[strings.Index(msg, "\n[control]"):]
	c := Default()
	if _, err := toml.Decode(table, &c); err != nil {
		t.Fatalf("the example does not parse: %v\n%s", err, table)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("the example does not validate: %v", err)
	}
}

func TestControlNeedsItsFields(t *testing.T) {
	for name, tc := range map[string]struct {
		control Model
		want    string
	}{
		"model only (the old default url)": {Model{Model: "qwen3:8b", NumCtx: 81920}, "url is required: nibb has no built-in control model"},
		"url only (a --url flag)":          {Model{URL: "http://localhost:11434", NumCtx: 81920}, "model is required"},
	} {
		c := Default()
		c.Control = tc.control
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	for _, m := range []Model{
		{URL: "http://localhost:11434", Model: "qwen3:8b"},
		{Provider: "openai", URL: "https://api.example.com/v1", Model: "m", Credential: "k"},
	} {
		c := Default()
		c.Control = m
		if err := c.Validate(); err != nil {
			t.Fatalf("a control model without num_ctx: %v", err)
		}
	}
	if got := Default().WindowNumCtx(); got != 61440+16384 {
		t.Fatalf("WindowNumCtx %d", got)
	}
}
