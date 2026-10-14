package config

import (
	"strings"
	"testing"
)

// The terminal is experimental (ADR 48): off by default, on only by naming it,
// and a key under [experimental] that is not a feature is a mistake the file
// is refused for, as everywhere else in config.toml.
func TestExperimentalTerminalIsOffUnlessSet(t *testing.T) {
	if Default().Experimental.Terminal {
		t.Fatal("the terminal is on by default")
	}
	c, err := Parse([]byte("[experimental]\nterminal = true\n"))
	if err != nil || !c.Experimental.Terminal {
		t.Fatalf("the documented setting: %+v %v", c.Experimental, err)
	}
	if c, err = Parse([]byte("[approval]\nwrites = \"ask\"\n")); err != nil || c.Experimental.Terminal {
		t.Fatalf("a file without the table: %+v %v", c.Experimental, err)
	}
	if _, err := Parse([]byte("[experimental]\nterminals = true\n")); err == nil || !strings.Contains(err.Error(), "unknown keys") {
		t.Fatalf("a mistyped feature: %v", err)
	}
}
