package binding

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"strings"
	"unicode"

	"github.com/BurntSushi/toml"
	"github.com/fclaude/nodwyn/internal/policyfile"
)

// Exact constructs a single-call proposal. It accepts no wildcard, missing
// scope, URL, or control character. Existing hand-written rows remain governed
// by Load's compatibility rules; proposals are deliberately narrower. An
// OAuth2 credential's granted scopes go in the row, so it binds that token
// and no broader one.
func Exact(class, host, method, position string, scopes ...string) (Binding, error) {
	bad := func() (Binding, error) {
		return Binding{}, fmt.Errorf("bindings: incomplete or invalid exact call scope")
	}
	if class == "" || strings.TrimSpace(class) != class || strings.IndexFunc(class, unicode.IsControl) >= 0 {
		return bad()
	}
	if host == "" || strings.ContainsAny(host, "*/@?#[]\\") || strings.IndexFunc(host, unicode.IsSpace) >= 0 || strings.IndexFunc(host, unicode.IsControl) >= 0 {
		return bad()
	}
	if strings.Contains(host, ":") && net.ParseIP(host) == nil {
		return bad()
	}
	if !token(method) {
		return bad()
	}
	parts := strings.Split(position, ":")
	switch parts[0] {
	case "body":
		if len(parts) != 1 {
			return bad()
		}
	case "header":
		if len(parts) < 2 || len(parts) > 3 || !token(parts[1]) || (len(parts) == 3 && !token(parts[2])) {
			return bad()
		}
	case "query":
		if len(parts) != 2 || parts[1] == "" || strings.IndexFunc(parts[1], unicode.IsControl) >= 0 {
			return bad()
		}
	default:
		return bad()
	}
	for _, s := range scopes {
		if s == "" || strings.IndexFunc(s, unicode.IsSpace) >= 0 || strings.IndexFunc(s, unicode.IsControl) >= 0 {
			return bad()
		}
	}
	row := Binding{Class: class, Dest: host, Methods: []string{method}, Positions: []string{position}}
	if len(scopes) > 0 {
		row.Scopes = append([]string(nil), scopes...)
	}
	return row, nil
}
func token(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c <= 32 || c >= 127 || strings.ContainsRune("()<>@,;:\\\"/[]?={}", c) {
			return false
		}
	}
	return true
}

// AppendExact preserves the user's bytes and appends one TOML row. A sibling
// advisory lock coordinates nodwyn writers across processes; edits by other
// programs should use the same lock. No partial policy is ever published.
func AppendExact(ctx context.Context, path string, row Binding) error {
	if len(row.Methods) != 1 || len(row.Positions) != 1 {
		return fmt.Errorf("bindings: an exact row is required")
	}
	if _, err := Exact(row.Class, row.Dest, row.Methods[0], row.Positions[0], row.Scopes...); err != nil {
		return err
	}
	if path == "" {
		return fmt.Errorf("bindings: no persistence path")
	}
	return policyfile.Update(ctx, path, func(raw []byte) ([]byte, error) {
		table, err := parse(raw, path)
		if err != nil {
			return nil, err
		}
		if bound, _ := table.Lookup(row.Class, row.Dest, row.Methods[0], row.Positions[0], row.Scopes...); bound {
			return nil, ctx.Err()
		}
		var addition bytes.Buffer
		if err := toml.NewEncoder(&addition).Encode(Table{Bind: []Binding{row}}); err != nil {
			return nil, err
		}
		return append(append(append([]byte(nil), raw...), '\n'), addition.Bytes()...), nil
	})
}
