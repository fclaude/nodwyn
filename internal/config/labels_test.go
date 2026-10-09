package config

import (
	"slices"
	"strings"
	"testing"
)

// Labels are a model's words for what it is for. They parse, round-trip
// through the file Settings writes, are found for the default in its entry as
// its efforts are, and a badly spelled or repeated one is refused.
func TestModelLabels(t *testing.T) {
	const base = "[servers.local]\nprovider = \"ollama\"\nurl = \"http://localhost:11434\"\n"
	c, err := Parse([]byte(base + `
[control]
server = "local"
model = "big"
labels = ["strong"]

[models.small]
server = "local"
model = "small"
labels = ["fast", "subagent"]
`))
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Models["small"].Labels; !slices.Equal(got, []string{"fast", "subagent"}) {
		t.Errorf("small labels = %v", got)
	}
	if got := c.Control.Labels; !slices.Equal(got, []string{"strong"}) {
		t.Errorf("control labels = %v", got)
	}

	out, back, err := SetModel([]byte(base), "small", c.Models["small"])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `labels = ["fast", "subagent"]`) || !slices.Equal(back.Models["small"].Labels, []string{"fast", "subagent"}) {
		t.Errorf("labels did not round-trip:\n%s", out)
	}
	// The default chosen from the list keeps no copy of its entry's labels,
	// which would go stale when the entry changes: it goes by the entry's.
	_, back, err = SetControl(out, back.Models["small"].Choice())
	if err != nil {
		t.Fatal(err)
	}
	if len(back.Control.Labels) != 0 || !slices.Equal(back.LabelsOf(back.Control), []string{"fast", "subagent"}) {
		t.Errorf("[control] labels = %v, goes by %v", back.Control.Labels, back.LabelsOf(back.Control))
	}
	if got := c.LabelsOf(c.Control); !slices.Equal(got, []string{"strong"}) {
		t.Errorf("[control]'s own labels = %v", got)
	}

	for name, labels := range map[string]string{
		"upper case": `["Fast"]`,
		"a space":    `["very fast"]`,
		"empty":      `[""]`,
		"twice":      `["fast", "fast"]`,
	} {
		_, err := Parse([]byte(base + "[models.m]\nserver = \"local\"\nmodel = \"m\"\nlabels = " + labels + "\n"))
		if err == nil || !strings.Contains(err.Error(), "labels") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}
