package scheme

import (
	"slices"
	"strings"
)

// Taint is the provenance tag every value carries (SPEC L2, Phase 6): where
// the value came from, not what it contains. A tag is a short string such as
// "external:file", "external:shell" or "cred:jev-key"; a value may carry
// several, and the union of a call's arguments flows into its result.
//
// Representation: an immutable, sorted set of names. Copies of a Value may
// share the backing slice; constructors and Union never modify existing sets,
// and Tags returns a copy. Attribution has no process-global registry or cap.
//
// Scope, stated plainly (SPEC Risk 1): L2 stops the *model* piping a tool
// result into an egress by accident. It is not a boundary against a user,
// who may call (wipe v), and it does not survive a value being rebuilt
// character by character through predicates. The real egress lines are the
// binding check, fail-closed defaults, and ingress redaction.
type Taint struct {
	names []string
}

// Tag names for the sources the harness itself mints. TagMCP names the
// source, not the transport: an MCP server is a third party the person
// approved (ADR 23), whatever carries its replies.
const (
	TagFile  = "external:file"
	TagShell = "external:shell"
	TagHTTP  = "external:http"
	TagMCP   = "external:mcp"
)

// CredTag is the tag a credential handle of the given class carries.
func CredTag(class string) string { return "cred:" + class }

// externalPrefix and credPrefix classify a tag.
const (
	externalPrefix = "external:"
	credPrefix     = "cred:"
)

// TaintOf returns the taint carrying exactly the named tags.
func TaintOf(names ...string) Taint {
	names = slices.Clone(names)
	slices.Sort(names)
	return Taint{names: slices.Compact(names)}
}

// Union combines taints without changing any input's backing storage.
func Union(ts ...Taint) Taint {
	var names []string
	for _, t := range ts {
		names = append(names, t.names...)
	}
	return TaintOf(names...)
}

// IsClean reports whether the value carries no taint.
func (t Taint) IsClean() bool { return len(t.names) == 0 }

// Has reports whether t carries the named tag.
func (t Taint) Has(name string) bool { return slices.Contains(t.names, name) }

// Tags returns a copy of the tag names in sorted order.
func (t Taint) Tags() []string { return slices.Clone(t.names) }

// String renders the tags for a log line or a guard reason.
func (t Taint) String() string { return strings.Join(t.Tags(), ",") }

// Externals returns the external:* tags t carries.
func (t Taint) Externals() []string { return t.filter(externalPrefix) }

// Creds returns the credential classes t carries (without the prefix).
func (t Taint) Creds() []string {
	var out []string
	for _, n := range t.filter(credPrefix) {
		out = append(out, strings.TrimPrefix(n, credPrefix))
	}
	return out
}

func (t Taint) filter(prefix string) []string {
	var out []string
	for _, n := range t.names {
		if strings.HasPrefix(n, prefix) {
			out = append(out, n)
		}
	}
	return out
}

// Tainted returns v with the given tags added.
func Tainted(v Value, t Taint) Value {
	v.Taint = Union(v.Taint, t)
	return v
}
