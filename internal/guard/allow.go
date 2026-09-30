package guard

import (
	"context"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/fclaude/nodwyn/internal/scheme"
)

// CommandAllow holds the user's standing approvals for command lines: a
// list of argv prefixes ("go test", "git status", "rg") that may run without
// a confirm. It is the command-line analogue of the binding table — the
// user authorizes ahead of time, the guard applies it silently and logs it.
// Entries come from config, from the repository's .nodwyn/allow.toml, and from
// "a" answers during a session (not persisted).
type CommandAllow struct {
	mu       sync.RWMutex
	prefixes [][]string
}

// NewCommandAllow builds the checker from "prog sub ..." strings.
func NewCommandAllow(entries ...string) *CommandAllow {
	c := &CommandAllow{}
	for _, e := range entries {
		c.Add(e)
	}
	return c
}

// Add registers a prefix. Empty entries are ignored.
func (c *CommandAllow) Add(entry string) {
	f := strings.Fields(entry)
	if len(f) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, p := range c.prefixes {
		if equal(p, f) {
			return
		}
	}
	c.prefixes = append(c.prefixes, f)
}

// List returns the entries, sorted.
func (c *CommandAllow) List() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]string, len(c.prefixes))
	for i, p := range c.prefixes {
		out[i] = strings.Join(p, " ")
	}
	sort.Strings(out)
	return out
}

// Match returns the matching prefix for argv, or "".
func (c *CommandAllow) Match(argv []string) string {
	if len(argv) == 0 {
		return ""
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, p := range c.prefixes {
		if len(p) > len(argv) {
			continue
		}
		ok := filepath.Base(argv[0]) == p[0]
		for i := 1; ok && i < len(p); i++ {
			ok = argv[i] == p[i]
		}
		if ok {
			return strings.Join(p, " ")
		}
	}
	return ""
}

func (*CommandAllow) Name() string { return "command-allow" }

// Check returns a standing Allow for a matching command line. A standing
// allow outranks a Confirm from any other checker, never a Deny.
func (c *CommandAllow) Check(_ context.Context, p *scheme.Prim, args []scheme.Value) Decision {
	argv := CommandArgs(p, args)
	if m := c.Match(argv); m != "" {
		return Decision{Verdict: Allow, Standing: true, Reason: "standing approval: " + m}
	}
	return Decision{Verdict: Allow}
}

// Prefix suggests the standing-approval entry for a command line: the
// program and its first argument when that argument looks like a
// subcommand (go test, git diff), else the program alone.
func Prefix(argv []string) string {
	if len(argv) == 0 {
		return ""
	}
	prog := filepath.Base(argv[0])
	if len(argv) > 1 && !strings.HasPrefix(argv[1], "-") && !strings.Contains(argv[1], "/") && !strings.Contains(argv[1], ".") {
		return prog + " " + argv[1]
	}
	return prog
}

// Argv extracts the command line from a primitive application (for the
// core, which builds Prefix from a prompt).
func Argv(p *scheme.Prim, args []scheme.Value) []string { return CommandArgs(p, args) }

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
