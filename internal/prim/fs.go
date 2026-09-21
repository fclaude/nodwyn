package prim

import (
	"context"
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
			Doc: "Write a text file inside the repository, creating it or replacing its contents. Parent directories are created.",
			Params: []scheme.Param{
				{Name: "path", Type: "string", Doc: "path relative to the repository root", Role: "path"},
				{Name: "content", Type: "string", Doc: "the complete new contents", Role: "payload"},
			},
			Host:         true,
			Irreversible: true,
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

// WritePreview is the guard checker that shows a diff before an fs-write is
// confirmed. It looks only at metadata roles (path + payload on an
// irreversible primitive), not at the name.
type WritePreview struct {
	Pin guard.RepoPin
}

func (WritePreview) Name() string { return "write-preview" }

func (w WritePreview) Check(_ context.Context, p *scheme.Prim, args []scheme.Value) guard.Decision {
	if !p.Meta.Irreversible {
		return guard.Decision{Verdict: guard.Allow}
	}
	path, payload := -1, -1
	for i, pa := range p.Meta.Params {
		switch pa.Role {
		case "path":
			path = i
		case "payload":
			payload = i
		}
	}
	if path < 0 || payload < 0 || path >= len(args) || payload >= len(args) || args[path].Kind != scheme.KString || args[payload].Kind != scheme.KString {
		return guard.Decision{Verdict: guard.Allow}
	}
	abs := w.Pin.Resolve(args[path].S)
	if abs == "" {
		return guard.Decision{Verdict: guard.Allow} // RepoPin denies it
	}
	old, err := os.ReadFile(abs)
	newText := args[payload].S
	var detail string
	switch {
	case err != nil:
		lines := strings.Count(newText, "\n")
		detail = fmt.Sprintf("new file %s (%d lines, %d bytes)\n%s", args[path].S, lines, len(newText), Unified(args[path].S, "", newText, 3))
	case string(old) == newText:
		detail = fmt.Sprintf("%s: contents unchanged", args[path].S)
	default:
		detail = Unified(args[path].S, string(old), newText, 3)
	}
	// The verdict is Confirm so the detail travels with the prompt; MetaPolicy
	// already asks for one, and the guard merges the reasons.
	return guard.Decision{Verdict: guard.Confirm, Reason: "writes " + args[path].S, Detail: detail}
}
