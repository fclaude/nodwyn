package scheme

import (
	"sort"
	"strings"
	"sync"
)

// Taint is the provenance tag every value carries (SPEC L2, Phase 6): where
// the value came from, not what it contains. A tag is a short string such as
// "external:file", "external:shell" or "cred:jev-key"; a value may carry
// several, and the union of a call's arguments flows into its result.
//
// Representation: a 64-bit set over an interned tag table, so a Value stays
// a value type, a union is one OR, and "clean" stays a zero comparison. The
// 64th slot is the overflow bit: a process that mints more than 63 distinct
// tags marks the rest as tainted-but-unattributed, which is less precise and
// never less safe.
//
// Scope, stated plainly (SPEC Risk 1): L2 stops the *model* piping a tool
// result into an egress by accident. It is not a boundary against a user,
// who may call (wipe v), and it does not survive a value being rebuilt
// character by character through predicates. The real egress lines are the
// binding check, fail-closed defaults, and ingress redaction.
type Taint struct {
	bits uint64
}

const (
	overflowBit = uint64(1) << 63
	maxTags     = 63
)

// Tag names for the sources the harness itself mints.
const (
	TagFile  = "external:file"
	TagShell = "external:shell"
	TagHTTP  = "external:http"
	TagEnv   = "external:env"
)

// CredTag is the tag a credential handle of the given class carries.
func CredTag(class string) string { return "cred:" + class }

// ExternalPrefix and CredPrefix classify a tag.
const (
	ExternalPrefix = "external:"
	CredPrefix     = "cred:"
)

var tags struct {
	mu   sync.RWMutex
	bit  map[string]uint64
	name []string // index i holds the tag for bit 1<<i
}

// TaintOf returns the taint carrying exactly the named tags.
func TaintOf(names ...string) Taint {
	var t Taint
	for _, n := range names {
		t.bits |= bitFor(n)
	}
	return t
}

func bitFor(name string) uint64 {
	tags.mu.RLock()
	b, ok := tags.bit[name]
	tags.mu.RUnlock()
	if ok {
		return b
	}
	tags.mu.Lock()
	defer tags.mu.Unlock()
	if b, ok := tags.bit[name]; ok {
		return b
	}
	if tags.bit == nil {
		tags.bit = make(map[string]uint64)
	}
	if len(tags.name) >= maxTags {
		return overflowBit
	}
	b = uint64(1) << uint(len(tags.name))
	tags.bit[name] = b
	tags.name = append(tags.name, name)
	return b
}

// Union combines taints.
func Union(ts ...Taint) Taint {
	var out Taint
	for _, t := range ts {
		out.bits |= t.bits
	}
	return out
}

// IsClean reports whether the value carries no taint.
func (t Taint) IsClean() bool { return t.bits == 0 }

// Has reports whether t carries the named tag.
func (t Taint) Has(name string) bool { return t.bits&bitFor(name) != 0 }

// Tags returns the tag names, sorted. An overflowed taint reports
// "tainted:unattributed".
func (t Taint) Tags() []string {
	if t.bits == 0 {
		return nil
	}
	tags.mu.RLock()
	defer tags.mu.RUnlock()
	var out []string
	for i, n := range tags.name {
		if t.bits&(uint64(1)<<uint(i)) != 0 {
			out = append(out, n)
		}
	}
	if t.bits&overflowBit != 0 {
		out = append(out, "tainted:unattributed")
	}
	sort.Strings(out)
	return out
}

// String renders the tags for a log line or a guard reason.
func (t Taint) String() string { return strings.Join(t.Tags(), ",") }

// Externals returns the external:* tags t carries.
func (t Taint) Externals() []string { return t.filter(ExternalPrefix) }

// Creds returns the credential classes t carries (without the prefix).
func (t Taint) Creds() []string {
	var out []string
	for _, n := range t.filter(CredPrefix) {
		out = append(out, strings.TrimPrefix(n, CredPrefix))
	}
	return out
}

func (t Taint) filter(prefix string) []string {
	var out []string
	for _, n := range t.Tags() {
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
