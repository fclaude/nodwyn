// Package binding owns the user-defined egress authorization table.
// It loads bindings and matches classes, destinations, methods and positions.
// The guard interprets Scheme calls and decides what an unbound call does.
package binding

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/BurntSushi/toml"
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
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Table{}, nil
	}
	if err != nil {
		return Table{}, fmt.Errorf("bindings: %s: %w", path, err)
	}
	return parse(raw, path)
}

func parse(raw []byte, path string) (Table, error) {
	var t Table
	md, err := toml.Decode(string(raw), &t)
	if err != nil {
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

// Position splits a binding position into (where, name, prefix). where is
// one of "header", "query", "body"; name is the header or query key; prefix
// is the auth scheme plus a space for a position like
// "header:Authorization:Bearer", else "".
func Position(pos string) (where, name, prefix string) {
	parts := strings.SplitN(pos, ":", 3)
	where = parts[0]
	if len(parts) > 1 {
		name = parts[1]
	}
	if len(parts) > 2 && parts[2] != "" {
		prefix = parts[2] + " "
	}
	return where, name, prefix
}

// HasDestination reports whether any binding authorizes something at host.
// It deliberately ignores credential class, method and position (L2 policy).
func (t Table) HasDestination(host string) bool {
	if host == "" {
		return false
	}
	for _, row := range t.Bind {
		if hostMatches(row.Dest, host) {
			return true
		}
	}
	return false
}
