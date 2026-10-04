package config

import (
	"strings"
	"testing"
)

// Saving a parsed configuration retains the dialect where the person put
// it, including when the console replaces a server or the control model.
func TestDialectSurvivesConfigEdits(t *testing.T) {
	for _, raw := range []string{
		"[control]\nprovider = \"openai\"\ndialect = \"cerebras\"\nurl = \"https://example.test/v1\"\nmodel = \"m\"\n",
		"[servers.fast]\nprovider = \"openai\"\ndialect = \"cerebras\"\nurl = \"https://example.test/v1\"\n[control]\nserver = \"fast\"\nmodel = \"m\"\n[models.coder]\nserver = \"fast\"\nmodel = \"coder\"\n",
	} {
		cfg, err := Parse([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		out, cfg, err := SetControl([]byte(raw), cfg.Control)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Control.Server != "" {
			out, cfg, err = SetServer(out, "fast", cfg.Servers["fast"])
			if err != nil {
				t.Fatal(err)
			}
		}
		if strings.Count(string(out), `dialect = "cerebras"`) != 1 {
			t.Fatalf("dialect lost or duplicated:\n%s", out)
		}
		for _, m := range []Model{cfg.Control, cfg.Models["coder"]} {
			if m.Model == "" {
				continue
			}
			m.Server = ""
			out, _, err = SetControl(nil, m)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(out), `dialect = "cerebras"`) {
				t.Fatalf("not inherited: %s", out)
			}
		}
	}
}

func TestDialectConfigRefusals(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{"[servers.s]\nprovider = \"openai\"\nurl = \"https://example.test\"\ndialect = \"typo\"\n", `dialect "typo" is not supported`},
		{"[servers.s]\nurl = \"http://localhost:11434\"\ndialect = \"cerebras\"\n", "dialect is for openai servers"},
		{"[control]\nurl = \"http://localhost:11434\"\nmodel = \"m\"\ndialect = \"compatible\"\n", "dialect is for openai servers"},
		{"[servers.s]\nprovider = \"openai\"\nurl = \"https://example.test\"\ndialect = \"cerebras\"\n[control]\nserver = \"s\"\nmodel = \"m\"\ndialect = \"compatible\"\n", `dialect comes from server "s"`},
	} {
		if _, err := Parse([]byte(tc.raw)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v", tc.raw, err)
		}
	}
}
