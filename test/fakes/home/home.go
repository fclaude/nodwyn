// Package home gives a test binary a home directory of its own, so its
// tests do not depend on the machine they run on: on where TMPDIR is
// (nodwyn's denylist protects its directories under HOME, and when nodwyn
// runs its own tests TMPDIR was a session's scratch directory inside its
// data directory, where every test repository was refused; since 2026-10-16
// it is in the system's temporary directory), on the user's
// git configuration (commit signing through an agent a test cannot reach),
// or on their nodwyn configuration.
package home

import (
	"os"
)

// Isolate points HOME at a new empty directory, with the XDG directories
// and git's global and system configuration starting from nothing, and git
// given an identity unless the environment has one. It also sets
// NODWYN_PROFILE=full: the sessions these tests build run the default loop
// the tests were written against (an explorer and a planner for a run, the
// finish review, the whole catalogue of tools) and not the lean one that is
// the binary's default for an unattended session (ADR 32, amended
// 2026-10-17). A test of the lean profile sets the variable itself
// (t.Setenv). Call it in TestMain before m.Run; the function it returns
// removes the directory.
func Isolate() func() {
	dir, err := os.MkdirTemp("", "nodwyn-test-home-")
	if err != nil {
		panic("home: " + err.Error())
	}
	os.Setenv("HOME", dir)
	for _, k := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "GIT_CONFIG_GLOBAL"} {
		os.Unsetenv(k)
	}
	os.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	os.Setenv("NODWYN_PROFILE", "full")
	for k, v := range map[string]string{"GIT_AUTHOR_NAME": "nodwyn test", "GIT_AUTHOR_EMAIL": "test@nodwyn.invalid", "GIT_COMMITTER_NAME": "nodwyn test", "GIT_COMMITTER_EMAIL": "test@nodwyn.invalid"} {
		if os.Getenv(k) == "" {
			os.Setenv(k, v)
		}
	}
	return func() { os.RemoveAll(dir) }
}
