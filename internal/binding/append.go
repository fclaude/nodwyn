package binding

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"regexp"
	"slices"
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
	case "env":
		// A contained MCP server's environment variable (ADR 23 W3b).
		if len(parts) != 2 || !envName(parts[1]) {
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

// envName reports whether s is a portable environment variable name.
func envName(s string) bool {
	if s == "" || len(s) > 64 || s[0] >= '0' && s[0] <= '9' {
		return false
	}
	for _, c := range s {
		if !(c == '_' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
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

var (
	bindHeader  = regexp.MustCompile(`^[ \t]*\[\[[ \t]*bind[ \t]*\]\][ \t]*(#.*)?$`)
	tableHeader = regexp.MustCompile(`^[ \t]*\[`)
)

// RemoveExact narrows the file by taking out the rows that allow class at
// dest with exactly these positions and scopes, whatever their methods: the
// row the console lists. Every other byte stays as the user wrote it,
// comments included. It cuts only a [[bind]] table it can delimit and read on
// its own, and checks the result: a row held in any other form (an inline
// array) is not guessed at, and the file is left as it was with an error that
// says to edit it. It returns how many rows it removed, none leaving the file
// unchanged.
func RemoveExact(ctx context.Context, path, class, dest string, positions, scopes []string) (int, error) {
	dest = strings.ToLower(strings.TrimSpace(dest))
	return remove(ctx, path, fmt.Sprintf("%s at %s", class, dest), func(b Binding) bool {
		return b.Class == class && strings.ToLower(b.Dest) == dest && sameSet(b.Positions, positions) && sameSet(b.Scopes, scopes)
	})
}

// RemoveClass takes every row that allows class out of the file, as
// RemoveExact does one: a credential removed takes its authority with it, so
// the next credential given the class does not inherit it.
func RemoveClass(ctx context.Context, path, class string) (int, error) {
	return remove(ctx, path, class, func(b Binding) bool { return b.Class == class })
}

// remove cuts out of the file each [[bind]] table whose row matches, and
// refuses when a matching row is left in a form it cannot cut; what names
// the rows in that refusal.
func remove(ctx context.Context, path, what string, matches func(Binding) bool) (int, error) {
	if path == "" {
		return 0, fmt.Errorf("bindings: no persistence path")
	}
	removed := 0
	err := policyfile.Update(ctx, path, func(raw []byte) ([]byte, error) {
		removed = 0
		before, err := parse(raw, path)
		if err != nil {
			return nil, err
		}
		lines := strings.SplitAfter(string(raw), "\n")
		var out strings.Builder
		for i := 0; i < len(lines); {
			if !bindHeader.MatchString(strings.TrimRight(lines[i], "\r\n")) {
				out.WriteString(lines[i])
				i++
				continue
			}
			// The table runs to the next table header. Comments and blank
			// lines at its end are the next row's, or the file's: they stay.
			end := i + 1
			for end < len(lines) && !tableHeader.MatchString(lines[end]) {
				end++
			}
			last := end
			for last > i+1 && (strings.TrimSpace(lines[last-1]) == "" || strings.HasPrefix(strings.TrimSpace(lines[last-1]), "#")) {
				last--
			}
			one, err := parse([]byte(strings.Join(lines[i:last], "")), path)
			if err == nil && len(one.Bind) == 1 && matches(one.Bind[0]) {
				removed++
			} else {
				out.WriteString(strings.Join(lines[i:last], ""))
			}
			out.WriteString(strings.Join(lines[last:end], ""))
			i = end
		}
		if removed == 0 {
			// Nothing cut out as text: say so when a row that matches is still
			// there in a form this cannot cut.
			for _, b := range before.Bind {
				if matches(b) {
					return nil, fmt.Errorf("bindings: that row is written in a form the console cannot edit: change it in %s", path)
				}
			}
			return nil, nil
		}
		after, err := parse([]byte(out.String()), path)
		if err != nil {
			return nil, err
		}
		for _, b := range after.Bind {
			if matches(b) {
				return nil, fmt.Errorf("bindings: a row for %s is written in a form the console cannot edit: change it in %s", what, path)
			}
		}
		if len(before.Bind)-len(after.Bind) != removed {
			return nil, fmt.Errorf("bindings: removing the row would have changed other rows: change %s by hand", path)
		}
		return []byte(out.String()), nil
	})
	if err != nil {
		return 0, err
	}
	return removed, nil
}

// sameSet reports whether a and b hold the same words, in any order.
func sameSet(a, b []string) bool {
	x, y := slices.Clone(a), slices.Clone(b)
	slices.Sort(x)
	slices.Sort(y)
	return slices.Equal(slices.Compact(x), slices.Compact(y))
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
