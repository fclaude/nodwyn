package config

import (
	"strings"
	"testing"
)

// [loop] profile is auto unless the file says lean or full, and a file that
// names another is refused.
func TestLoopProfileIsAutoUnlessSet(t *testing.T) {
	if got := Default().Loop.Profile; got != ProfileAuto {
		t.Fatalf("default profile %q", got)
	}
	for _, name := range []string{"auto", "lean", "full"} {
		c, err := Parse([]byte("[loop]\nprofile = \"" + name + "\"\n"))
		if err != nil || c.Loop.Profile != name {
			t.Errorf("profile %s: %+v %v", name, c.Loop, err)
		}
	}
	c, err := Parse([]byte("[loop]\nshell_timeout = \"5m\"\n"))
	if err != nil || c.Loop.Profile != ProfileAuto {
		t.Errorf("a file without the key: %+v %v", c.Loop, err)
	}
	if _, err := Parse([]byte("[loop]\nprofile = \"leaner\"\n")); err == nil || !strings.Contains(err.Error(), `loop.profile must be "auto", "lean" or "full", got "leaner"`) {
		t.Errorf("an unknown profile: %v", err)
	}
	if v, ok := Default().Value("loop", "profile"); !ok || v != "auto" {
		t.Errorf("Value(loop, profile) = %v %v", v, ok)
	}
}

// ResolveProfile: the environment over the file, and auto by who approves.
func TestResolveProfile(t *testing.T) {
	for _, c := range []struct {
		file, env  string
		unattended bool
		want       string
	}{
		{"auto", "", true, ProfileLean},
		{"auto", "", false, ProfileFull},
		{"", "", true, ProfileLean},
		{"", "", false, ProfileFull},
		{"lean", "", false, ProfileLean},
		{"full", "", true, ProfileFull},
		{"full", "lean", false, ProfileLean},
		{"lean", "full", true, ProfileFull},
		{"full", "auto", true, ProfileLean},
		{"lean", "auto", false, ProfileFull},
	} {
		got, err := Loop{Profile: c.file}.ResolveProfile(c.env, c.unattended)
		if err != nil || got != c.want {
			t.Errorf("file %q, env %q, unattended %v: %q %v, want %q", c.file, c.env, c.unattended, got, err, c.want)
		}
	}
	if _, err := (Loop{Profile: "auto"}).ResolveProfile("quick", true); err == nil || !strings.Contains(err.Error(), ProfileEnv+" must be") {
		t.Errorf("an unknown profile in the environment: %v", err)
	}
}
