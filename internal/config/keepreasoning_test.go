package config

import (
	"strings"
	"testing"
)

// keep_reasoning keeps a Responses model's reasoning to give back. It is read,
// lent to [control] by the model's own entry like its other settings,
// round-trips through the file Settings writes, and is refused where nothing
// gives reasoning back to keep (ADR 9, amended 2026-10-07).
func TestKeepReasoningParsesAndRoundTrips(t *testing.T) {
	const server = "[servers.plan]\nprovider = \"openai\"\ndialect = \"responses\"\nurl = \"https://api.openai.com/v1\"\n"
	c, err := Parse([]byte(server + `
[control]
server = "plan"
model = "gpt-6.1-sol"

[models.sol]
server = "plan"
model = "gpt-6.1-sol"
keep_reasoning = true
`))
	if err != nil {
		t.Fatal(err)
	}
	if c.Control.KeepReasoning != nil {
		t.Fatalf("[control] holds its own copy: %v", *c.Control.KeepReasoning)
	}
	tuned := c.Tuned(c.Control)
	if tuned.KeepReasoning == nil || !*tuned.KeepReasoning {
		t.Fatalf("[control] did not borrow keep_reasoning from its model's entry: %+v", tuned)
	}
	out, back, err := SetControl([]byte(server), tuned)
	if err != nil {
		t.Fatal(err)
	}
	if back.Control.KeepReasoning == nil || !*back.Control.KeepReasoning || !strings.Contains(string(out), "keep_reasoning = true\n") {
		t.Fatalf("not written back:\n%s", out)
	}
}

func TestKeepReasoningRefusals(t *testing.T) {
	for name, toml := range map[string]string{
		"chat completions": "[control]\nprovider = \"openai\"\nurl = \"https://h/v1\"\nmodel = \"m\"\nkeep_reasoning = true\n",
		"anthropic":        "[control]\nprovider = \"anthropic\"\nurl = \"https://api.anthropic.com/v1\"\nmodel = \"m\"\nkeep_reasoning = true\n",
		"ollama":           "[control]\nurl = \"http://h\"\nmodel = \"m\"\nkeep_reasoning = false\n",
	} {
		if _, err := Parse([]byte(toml)); err == nil || !strings.Contains(err.Error(), "keep_reasoning is for openai servers with dialect = \"responses\"") {
			t.Errorf("%s: %v", name, err)
		}
	}
}
