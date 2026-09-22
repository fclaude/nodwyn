// Package binding is the egress authority (SPEC "Egress authority"): the
// user-owned binding table and the extraction of a call's egress facts.
// A secret class is authorized for exactly the (dest, positions, methods) it
// is bound to; anything else is unbound.
//
// This package owns the table and the lookup only. It imports the
// interpreter value type (to read a call's credential/dest/method) but not
// the guard, so the guard can consume the check without a cycle. Deciding
// what an unbound call does (deny or confirm) is the guard's job (ADR 6).
package binding

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/fclaude/nibb/internal/scheme"
)

// Binding is one row of the binding table: a secret class authorized for
// exactly these destinations, positions and methods.
type Binding struct {
	Class     string   `toml:"class"`     // e.g. "openai-key"
	Dest      string   `toml:"dest"`      // host, e.g. "api.openai.com"
	Positions []string `toml:"positions"` // e.g. "header:Authorization", "query:key", "body"
	Methods   []string `toml:"methods"`   // e.g. "POST"
}

// Table is bindings.toml. A missing file means an empty table, which by
// ADR 6 denies every secret-bearing egress.
type Table struct {
	Bind []Binding `toml:"bind"`
}

// Load reads the binding table from path. A missing file is an empty table.
func Load(path string) (Table, error) {
	var t Table
	md, err := toml.DecodeFile(path, &t)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Table{}, nil
		}
		return Table{}, fmt.Errorf("bindings: %s: %w", path, err)
	}
	if undec := md.Undecoded(); len(undec) > 0 {
		return Table{}, fmt.Errorf("bindings: %s: unknown keys: %v", path, undec)
	}
	for i, row := range t.Bind {
		if row.Class == "" || row.Dest == "" || len(row.Positions) == 0 || len(row.Methods) == 0 {
			return Table{}, fmt.Errorf("bindings: %s: row %d: class, dest, positions and methods are all required", path, i+1)
		}
	}
	return t, nil
}

// Facts is what E3 needs to judge a call: whether it carries a credential,
// the credential's class, and the destination host, method and requested
// injection position if present.
type Facts struct {
	HasCredential bool
	Class         string // credential class; "" if the argument is not a handle
	Host          string // destination hostname (lower-cased)
	Method        string // HTTP method, if the prim declares one
	Position      string // requested injection position, e.g. "header:Authorization"
}

// FactsOf extracts the egress facts of a primitive application.
func (t Table) FactsOf(p *scheme.Prim, args []scheme.Value) Facts {
	var f Facts
	if p.Meta.Egress {
		if i := credIndex(p, args); i >= 0 {
			// HasCredential is true only when the credential slot is actually
			// filled with a handle. A non-handle in the slot is not a valid
			// credential-bearing call: the guard's other checkers still apply,
			// but E3 has no binding to check.
			if class, ok := credClass(args[i]); ok {
				f.HasCredential = true
				f.Class = class
			}
		}
		f.Host = destHost(p, args)
		if f.Host == "" && p.Meta.EgressHost != "" {
			f.Host = strings.ToLower(p.Meta.EgressHost)
		}
		f.Method = methodArg(p, args)
		f.Position = positionArg(p, args)
	}
	return f
}

// Lookup reports whether (class, host, method, position) is bound by any
// row of the table. reason is set when it is not, for the guard's log and
// prompt.
func (t Table) Lookup(class, host, method, position string) (bound bool, reason string) {
	rows := t.rows(class)
	if len(rows) == 0 {
		return false, fmt.Sprintf("class %q has no binding", class)
	}
	if host == "" {
		return false, "no destination in the call"
	}
	hostOK := false
	for _, row := range rows {
		if !hostMatches(row.Dest, host) {
			continue
		}
		hostOK = true
		if method != "" && !member(row.Methods, method) {
			continue
		}
		if position != "" && !member(row.Positions, position) {
			continue
		}
		return true, ""
	}
	if !hostOK {
		return false, fmt.Sprintf("destination %s is not bound for class %s", host, class)
	}
	return false, fmt.Sprintf("method %s at position %s is not bound for class %s on %s", method, position, class, host)
}

// rows returns every binding row for class.
func (t Table) rows(class string) []Binding {
	var out []Binding
	for _, b := range t.Bind {
		if b.Class == class {
			out = append(out, b)
		}
	}
	return out
}

// credIndex returns the argument index of the primitive's credential, or -1.
func credIndex(p *scheme.Prim, args []scheme.Value) int {
	for i, pa := range p.Meta.Params {
		if pa.Role == "credential" && i < len(args) {
			return i
		}
	}
	return -1
}

// credClass returns the credential's class (from the handle), or "" if the
// argument is not a handle.
func credClass(v scheme.Value) (string, bool) {
	if v.Kind != scheme.KHandle {
		return "", false
	}
	return v.H.Class, true
}

// destHost returns the destination hostname: the URL argument's host if the
// primitive declares a "dest" role parameter, else "".
func destHost(p *scheme.Prim, args []scheme.Value) string {
	for i, pa := range p.Meta.Params {
		if pa.Role == "dest" && i < len(args) && args[i].Kind == scheme.KString {
			if u, err := url.Parse(args[i].S); err == nil {
				return strings.ToLower(u.Hostname())
			}
			return strings.ToLower(args[i].S)
		}
	}
	return ""
}

// methodArg returns the method argument if the primitive declares a
// "method" role parameter, else "".
func methodArg(p *scheme.Prim, args []scheme.Value) string {
	for i, pa := range p.Meta.Params {
		if pa.Role == "method" && i < len(args) && args[i].Kind == scheme.KString {
			return args[i].S
		}
	}
	return ""
}

// positionArg returns the requested injection position if the primitive
// declares a "position" role parameter, else "".
func positionArg(p *scheme.Prim, args []scheme.Value) string {
	for i, pa := range p.Meta.Params {
		if pa.Role == "position" && i < len(args) && args[i].Kind == scheme.KString {
			return args[i].S
		}
	}
	return ""
}

func hostMatches(bound, host string) bool {
	bound = strings.ToLower(bound)
	if bound == host {
		return true
	}
	// A wildcard "*." prefix covers all subdomains: "*.example.com" matches
	// "api.example.com".
	if strings.HasPrefix(bound, "*.") && strings.HasSuffix(host, strings.TrimPrefix(bound, "*")) {
		return true
	}
	return false
}

func member(set []string, v string) bool {
	for _, s := range set {
		if strings.EqualFold(s, v) {
			return true
		}
	}
	return false
}

// Position splits a binding position like "header:Authorization" into
// (where, name). where is one of "header", "query", "body".
func Position(pos string) (where, name string) {
	where, name, _ = strings.Cut(pos, ":")
	return where, name
}
