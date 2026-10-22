package guard

import (
	"context"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/fclaude/brote/scheme"
)

// CommandAllow holds the user's standing approvals for command lines: a
// list of argv prefixes ("go test", "git status", "rg") that may run without
// a confirm. It is the command-line analogue of the binding table — the
// user authorises ahead of time, the guard checks the arguments and logs it.
// Entries come from config, from the repository's .nodwyn/allow.toml, and from
// "a" answers during a session (not persisted).
type CommandAllow struct {
	mu         sync.RWMutex
	prefixes   [][]string
	identities []string
	root       string
	system     map[string]string
}

// NewCommandAllow builds the checker from "prog sub ..." strings.
func NewCommandAllow(entries ...string) *CommandAllow {
	return NewCommandAllowIn("", entries...)
}

// NewCommandAllowIn resolves executable identities when approval is installed.
// A bare name found inside the repository cannot inherit an approval for a
// system tool; an explicit repository-relative path may be approved separately.
func NewCommandAllowIn(root string, entries ...string) *CommandAllow {
	c := &CommandAllow{root: root, system: map[string]string{}}
	for name := range shells {
		c.system[name] = c.resolve(name)
	}
	for name := range filterFlags {
		c.system[name] = c.resolve(name)
	}
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
	c.identities = append(c.identities, c.resolve(f[0]))
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

// Match returns the matching prefix for argv, or "". A shell's -c line
// matches only when each command in it matches a prefix of its own (or is
// a filter a pipe feeds), never by the shell's name (shellCommands): their
// prefixes are returned joined by ", ".
func (c *CommandAllow) Match(argv []string) string {
	if cmds, wrapped := shellCommands(argv); wrapped {
		if cmds == nil || !c.systemCommand(argv[0]) {
			return ""
		}
		if argumentPath(argv[0], c.root, true) != "" {
			return ""
		}
		var matched []string
		for _, cmd := range cmds {
			for _, arg := range cmd.words {
				if argumentPath(arg, c.root, true) != "" {
					return ""
				}
			}
			if filter(cmd) && c.systemCommand(cmd.words[0]) {
				continue
			}
			m := c.match(cmd.words)
			if m == "" {
				return ""
			}
			if !slices.Contains(matched, m) {
				matched = append(matched, m)
			}
		}
		return strings.Join(matched, ", ")
	}
	return c.match(argv)
}

func (c *CommandAllow) match(argv []string) string {
	if commandArgumentRisk(argv, c.root) != "" {
		return ""
	}
	return c.rawMatch(argv)
}

func (c *CommandAllow) rawMatch(argv []string) string {
	if len(argv) == 0 {
		return ""
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	for j, p := range c.prefixes {
		if len(p) > len(argv) {
			continue
		}
		identity := c.resolve(argv[0])
		ok := identity != "" && identity == c.identities[j]
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
// allow outranks an ordinary Confirm, never a security Confirm or Deny.
func (c *CommandAllow) Check(_ context.Context, p *scheme.Prim, args []scheme.Value) Decision {
	argv := CommandArgs(p, args)
	if reason := c.argumentRisk(argv); reason != "" {
		return Decision{Verdict: Confirm, Security: true, Reason: reason, Detail: strings.Join(argv, " ")}
	}
	if m := c.Match(argv); m != "" {
		return Decision{Verdict: Allow, Standing: true, Reason: "standing approval: " + m}
	}
	return Decision{Verdict: Allow}
}

// Prefix suggests the standing-approval entry for a command line: the
// program and its first argument when that argument looks like a
// subcommand (go test, git diff), else the program alone. A shell's -c
// line is suggested by the command it runs, when it runs one (piped into
// filters or not); a line of several commands, or anything an interpreter
// runs, has none: "" offers no standing approval (shellline.go).
func Prefix(argv []string) string {
	if len(argv) == 0 {
		return ""
	}
	if argumentPath(argv[0], "", true) != "" {
		return ""
	}
	if cmds, wrapped := shellCommands(argv); wrapped {
		c := NewCommandAllow()
		if !c.systemCommand(argv[0]) {
			return ""
		}
		var run []string
		for _, cmd := range cmds {
			if !filter(cmd) {
				if run != nil {
					return ""
				}
				run = cmd.words
			}
		}
		if run == nil {
			return ""
		}
		return Prefix(run)
	}
	prog := argv[0]
	if interpreters[filepath.Base(prog)] {
		return ""
	}
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

// resolve rejects relative PATH entries (exec.ErrDot) and keeps a symlink's
// target as identity, so changing the link after approval does not transfer it.
func (c *CommandAllow) resolve(program string) string {
	explicit := strings.ContainsRune(program, filepath.Separator)
	path := program
	if !explicit {
		var err error
		path, err = exec.LookPath(program)
		if err != nil {
			return ""
		}
	} else if !filepath.IsAbs(path) {
		if c.root == "" {
			return ""
		}
		path = filepath.Join(c.root, path)
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil || !filepath.IsAbs(real) {
		return ""
	}
	if !explicit && c.root != "" {
		root, err := filepath.EvalSymlinks(c.root)
		if err != nil || real == root || strings.HasPrefix(real, root+string(filepath.Separator)) {
			return ""
		}
	}
	return real
}

func (c *CommandAllow) systemCommand(program string) bool {
	expected := c.system[filepath.Base(program)]
	return expected != "" && c.resolve(program) == expected
}
