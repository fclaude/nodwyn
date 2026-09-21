package prim

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/fclaude/nibb/internal/guard"
	"github.com/fclaude/nibb/internal/scheme"
)

func resolve(pin guard.RepoPin, c *scheme.Call, i int) (string, error) {
	raw, err := c.String(i)
	if err != nil {
		return "", err
	}
	abs := pin.Resolve(raw)
	if abs == "" {
		// The guard already denies this; the check here is defense in depth.
		return "", fmt.Errorf("path %s is outside the repository", raw)
	}
	return abs, nil
}

func fsRead(o Options, pin guard.RepoPin) *scheme.Prim {
	return &scheme.Prim{
		Name: "fs-read",
		Meta: scheme.Meta{
			Doc:    "Read a UTF-8 text file inside the repository and return its contents.",
			Params: []scheme.Param{{Name: "path", Type: "string", Doc: "path relative to the repository root", Role: "path"}},
			Host:   true,
		},
		Handler: func(c *scheme.Call) (scheme.Value, error) {
			abs, err := resolve(pin, c, 0)
			if err != nil {
				return scheme.Value{}, err
			}
			st, err := os.Stat(abs)
			if err != nil {
				return scheme.Value{}, err
			}
			if st.IsDir() {
				return scheme.Value{}, fmt.Errorf("%s is a directory (use fs-list)", c.Args[0].S)
			}
			if st.Size() > o.MaxReadBytes {
				return scheme.Value{}, fmt.Errorf("%s is %d bytes; fs-read reads at most %d", c.Args[0].S, st.Size(), o.MaxReadBytes)
			}
			b, err := os.ReadFile(abs)
			if err != nil {
				return scheme.Value{}, err
			}
			return scheme.Str(string(b)), nil
		},
	}
}

func fsWrite(o Options, pin guard.RepoPin) *scheme.Prim {
	return &scheme.Prim{
		Name: "fs-write",
		Meta: scheme.Meta{
			Doc: "Create a text file inside the repository or replace its entire contents. Parent directories are created. For changes to an existing file prefer fs-edit.",
			Params: []scheme.Param{
				{Name: "path", Type: "string", Doc: "path relative to the repository root", Role: "path"},
				{Name: "content", Type: "string", Doc: "the complete new contents", Role: "payload"},
			},
			Host:         true,
			Irreversible: true,
			Preview:      func(args []scheme.Value) string { return previewWrite(pin, args) },
		},
		Handler: func(c *scheme.Call) (scheme.Value, error) {
			abs, err := resolve(pin, c, 0)
			if err != nil {
				return scheme.Value{}, err
			}
			content, err := c.String(1)
			if err != nil {
				return scheme.Value{}, err
			}
			if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
				return scheme.Value{}, err
			}
			if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
				return scheme.Value{}, err
			}
			return scheme.Void, nil
		},
	}
}

func fsList(o Options, pin guard.RepoPin) *scheme.Prim {
	return &scheme.Prim{
		Name: "fs-list",
		Meta: scheme.Meta{
			Doc:    "List a directory inside the repository. Directories end with a slash. Returns a sorted list of names.",
			Params: []scheme.Param{{Name: "path", Type: "string", Doc: "directory relative to the repository root; \".\" for the root", Role: "path"}},
			Host:   true,
		},
		Handler: func(c *scheme.Call) (scheme.Value, error) {
			abs, err := resolve(pin, c, 0)
			if err != nil {
				return scheme.Value{}, err
			}
			entries, err := os.ReadDir(abs)
			if err != nil {
				return scheme.Value{}, err
			}
			names := make([]string, 0, len(entries))
			for _, e := range entries {
				n := e.Name()
				if e.IsDir() {
					n += "/"
				}
				names = append(names, n)
			}
			sort.Strings(names)
			out := make([]scheme.Value, len(names))
			for i, n := range names {
				out[i] = scheme.Str(n)
			}
			return scheme.List(out...), nil
		},
	}
}

// WritePreview is the guard checker that attaches a primitive's own
// preview (a diff) to the confirm prompt of an irreversible call.
type WritePreview struct{}

func (WritePreview) Name() string { return "write-preview" }

func (WritePreview) Check(_ context.Context, p *scheme.Prim, args []scheme.Value) guard.Decision {
	if !p.Meta.Irreversible || p.Meta.Preview == nil {
		return guard.Decision{Verdict: guard.Allow}
	}
	detail := p.Meta.Preview(args)
	if detail == "" {
		return guard.Decision{Verdict: guard.Allow}
	}
	// Confirm so the detail travels with the prompt; MetaPolicy already asks
	// for one and the guard merges the reasons.
	return guard.Decision{Verdict: guard.Confirm, Reason: "changes " + args[0].S, Detail: detail}
}

// previewWrite renders the diff an fs-write would produce.
func previewWrite(pin guard.RepoPin, args []scheme.Value) string {
	if len(args) < 2 || args[0].Kind != scheme.KString || args[1].Kind != scheme.KString {
		return ""
	}
	abs := pin.Resolve(args[0].S)
	if abs == "" {
		return "" // RepoPin denies it
	}
	old, err := os.ReadFile(abs)
	newText := args[1].S
	switch {
	case err != nil:
		return fmt.Sprintf("new file %s (%d lines, %d bytes)\n%s", args[0].S, strings.Count(newText, "\n"), len(newText), Unified(args[0].S, "", newText, 3))
	case string(old) == newText:
		return fmt.Sprintf("%s: contents unchanged", args[0].S)
	}
	return Unified(args[0].S, string(old), newText, 3)
}

// applyEdit computes the result of an fs-edit without writing it: exactly
// one occurrence of old must exist.
func applyEdit(content, old, replacement string) (string, error) {
	if old == "" {
		return "", errors.New("old text must not be empty")
	}
	switch n := strings.Count(content, old); n {
	case 0:
		return "", errors.New("old text not found; it must match exactly, including whitespace")
	case 1:
		return strings.Replace(content, old, replacement, 1), nil
	default:
		return "", fmt.Errorf("old text occurs %d times; include more context so it is unique", n)
	}
}

func fsEdit(o Options, pin guard.RepoPin) *scheme.Prim {
	preview := func(args []scheme.Value) string {
		if len(args) < 3 || args[0].Kind != scheme.KString || args[1].Kind != scheme.KString || args[2].Kind != scheme.KString {
			return ""
		}
		abs := pin.Resolve(args[0].S)
		if abs == "" {
			return ""
		}
		cur, err := os.ReadFile(abs)
		if err != nil {
			return "cannot read " + args[0].S + ": " + err.Error()
		}
		next, err := applyEdit(string(cur), args[1].S, args[2].S)
		if err != nil {
			return "edit would fail: " + err.Error()
		}
		return Unified(args[0].S, string(cur), next, 3)
	}
	return &scheme.Prim{
		Name: "fs-edit",
		Meta: scheme.Meta{
			Doc: "Edit a text file inside the repository by replacing exactly one occurrence of old with new. old must match the file exactly (including whitespace) and be unique; include surrounding lines to disambiguate. Prefer this over fs-write for changes to existing files.",
			Params: []scheme.Param{
				{Name: "path", Type: "string", Doc: "path relative to the repository root", Role: "path"},
				{Name: "old", Type: "string", Doc: "the exact text to replace (must occur exactly once)"},
				{Name: "new", Type: "string", Doc: "the replacement text", Role: "payload"},
			},
			Host:         true,
			Irreversible: true,
			Preview:      preview,
		},
		Handler: func(c *scheme.Call) (scheme.Value, error) {
			abs, err := resolve(pin, c, 0)
			if err != nil {
				return scheme.Value{}, err
			}
			old, err := c.String(1)
			if err != nil {
				return scheme.Value{}, err
			}
			repl, err := c.String(2)
			if err != nil {
				return scheme.Value{}, err
			}
			cur, err := os.ReadFile(abs)
			if err != nil {
				return scheme.Value{}, err
			}
			next, err := applyEdit(string(cur), old, repl)
			if err != nil {
				return scheme.Value{}, err
			}
			st, _ := os.Stat(abs)
			mode := os.FileMode(0o644)
			if st != nil {
				mode = st.Mode().Perm()
			}
			if err := os.WriteFile(abs, []byte(next), mode); err != nil {
				return scheme.Value{}, err
			}
			return scheme.Void, nil
		},
	}
}
