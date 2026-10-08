package config

import (
	"strings"
	"testing"
)

// keep_reasoning keeps a Responses model's reasoning to give back. It is false
// unless set, read, lent to [control] by the model's own entry like its other
// settings, and round-trips through the file Settings writes (ADR 9, amended
// 2026-10-07).
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
	if c.Control.KeepReasoning {
		t.Fatal("[control] holds its own copy")
	}
	if d := Default(); d.Control.KeepReasoning {
		t.Fatal("the default keeps reasoning")
	}
	tuned := c.Tuned(c.Control)
	if !tuned.KeepReasoning {
		t.Fatalf("[control] did not borrow keep_reasoning from its model's entry: %+v", tuned)
	}
	out, back, err := SetControl([]byte(server), tuned)
	if err != nil {
		t.Fatal(err)
	}
	if !back.Control.KeepReasoning || !strings.Contains(string(out), "keep_reasoning = true\n") {
		t.Fatalf("not written back:\n%s", out)
	}
}

// true is refused where nothing gives reasoning back to keep; false, the
// default, means the same everywhere and is taken anywhere.
func TestKeepReasoningRefusals(t *testing.T) {
	for name, toml := range map[string]string{
		"chat completions": "[control]\nprovider = \"openai\"\nurl = \"https://h/v1\"\nmodel = \"m\"\nkeep_reasoning = true\n",
		"anthropic":        "[control]\nprovider = \"anthropic\"\nurl = \"https://api.anthropic.com/v1\"\nmodel = \"m\"\nkeep_reasoning = true\n",
		"ollama":           "[control]\nurl = \"http://h\"\nmodel = \"m\"\nkeep_reasoning = true\n",
	} {
		if _, err := Parse([]byte(toml)); err == nil || !strings.Contains(err.Error(), "keep_reasoning is for openai servers with dialect = \"responses\"") {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := Parse([]byte("[control]\nurl = \"http://h\"\nmodel = \"m\"\nkeep_reasoning = false\n")); err != nil {
		t.Errorf("false refused: %v", err)
	}
}
