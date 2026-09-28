package prim

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

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

// readRangeLines is how many lines a ranged fs-read returns when no count
// is given.
const readRangeLines = 200

func fsRead(o Options, pin guard.RepoPin) *scheme.Prim {
	return &scheme.Prim{
		Name: "fs-read",
		Meta: scheme.Meta{
			Doc: "Read a UTF-8 text file inside the repository. Without start, returns the whole file. With start (a 1-based line number) and count (default 200), returns a header [lines A-B of N] and those lines exactly as in the file, without line numbers. Read files over about 9 KB in ranges: a whole read that large is shortened in your context. index-grep gives line numbers to start from.",
			Params: []scheme.Param{
				{Name: "path", Type: "string", Doc: "path relative to the repository root", Role: "path"},
				{Name: "start", Type: "integer", Doc: "first line to read, 1-based; omit both start and count to read the whole file", Optional: true},
				{Name: "count", Type: "integer", Doc: "how many lines to read from start (default 200)", Optional: true},
			},
			Host:        true,
			TaintSource: scheme.TagFile,
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
			if len(c.Args) > 1 {
				start, err := c.Int(1)
				if err != nil {
					return scheme.Value{}, err
				}
				count := int64(readRangeLines)
				if len(c.Args) > 2 {
					if count, err = c.Int(2); err != nil {
						return scheme.Value{}, err
					}
				}
				text, err := readLines(abs, c.Args[0].S, start, count, o.MaxRangeBytes)
				if err != nil {
					return scheme.Value{}, err
				}
				return scheme.Str(text), nil
			}
			if st.Size() > o.MaxReadBytes {
				return scheme.Value{}, fmt.Errorf("%s is %d bytes; fs-read reads at most %d at once: read it in ranges with start and count", c.Args[0].S, st.Size(), o.MaxReadBytes)
			}
			b, err := os.ReadFile(abs)
			if err != nil {
				return scheme.Value{}, err
			}
			return scheme.Str(string(b)), nil
		},
	}
}

// readLines returns lines start..start+count-1 of the file at abs (shown as
// name) under a header "[lines A-B of N]", each line exactly as in the file,
// its line ending included, so a copy of it matches for fs-edit. Output
// stops at a whole line once it would pass budget bytes, and the header's B
// says where it stopped; a first line longer than budget is cut and the
// header says so. The file is streamed, so its size is not limited.
func readLines(abs, name string, start, count int64, budget int) (string, error) {
	if start == 0 {
		start = 1 // start skipped (count given alone) or 0: from the top
	}
	if start < 1 {
		return "", fmt.Errorf("start is a 1-based line number, got %d", start)
	}
	if count < 1 {
		return "", fmt.Errorf("count must be at least 1, got %d", count)
	}
	f, err := os.Open(abs)
	if err != nil {
		return "", err
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 64<<10)
	var body strings.Builder
	var total, first, end int64 // lines in the file; the lines returned
	stopped, cut := false, false
	for {
		text, err := r.ReadString('\n')
		if err != nil && err != io.EOF {
			return "", err
		}
		if text == "" {
			break
		}
		total++
		if stopped || total < start || total >= start+count {
			continue
		}
		if body.Len()+len(text) > budget {
			if first == 0 {
				// One line longer than the budget: its start, cut at a
				// UTF-8 boundary.
				n := budget
				for n > 0 && !utf8.RuneStart(text[n]) {
					n--
				}
				body.WriteString(text[:n])
				first, end, cut = total, total, true
			}
			stopped = true
			continue
		}
		if first == 0 {
			first = total
		}
		body.WriteString(text)
		end = total
	}
	switch {
	case total == 0:
		return "", fmt.Errorf("%s is empty", name)
	case first == 0:
		return "", fmt.Errorf("start %d is past the end of %s (%d lines)", start, name, total)
	case cut:
		return fmt.Sprintf("[line %d of %d, cut at %d bytes]\n", first, total, budget) + body.String(), nil
	}
	return fmt.Sprintf("[lines %d-%d of %d]\n", first, end, total) + body.String(), nil
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
			Doc:         "List a directory inside the repository. Directories end with a slash. Returns a sorted list of names.",
			Params:      []scheme.Param{{Name: "path", Type: "string", Doc: "directory relative to the repository root; \".\" for the root", Role: "path"}},
			Host:        true,
			TaintSource: scheme.TagFile,
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
// one occurrence of old must exist, or with all at least one, and every
// occurrence is replaced.
func applyEdit(content, old, replacement string, all bool) (string, error) {
	if old == "" {
		return "", errors.New("old text must not be empty")
	}
	switch n := strings.Count(content, old); {
	case n == 0:
		return "", notFound(content, old)
	case all:
		return strings.ReplaceAll(content, old, replacement), nil
	case n == 1:
		return strings.Replace(content, old, replacement, 1), nil
	default:
		return "", fmt.Errorf("old text occurs %d times; include more context so it is unique, or pass replace_all to replace every one", n)
	}
}

// editHintBytes bounds the file text a not-found error quotes.
const editHintBytes = 2000

// notFound explains a failed match. When old matches exactly one place
// once whitespace is ignored (trailing spaces, tabs against spaces,
// indentation, CRLF), the error names those lines and quotes them exactly,
// so the next try copies the file's own text instead of guessing again.
func notFound(content, old string) error {
	const base = "old text not found; it must match exactly, including whitespace"
	lines := strings.SplitAfter(content, "\n")
	want := normalizedLines(old)
	if len(want) == 0 {
		return errors.New(base)
	}
	var at []int
	for i := 0; i+len(want) <= len(lines); i++ {
		ok := true
		for k, w := range want {
			if normalizeLine(lines[i+k]) != w {
				ok = false
				break
			}
		}
		if ok {
			at = append(at, i)
		}
	}
	switch len(at) {
	case 0:
		return errors.New(base + "; read the region again with fs-read (start, count) and copy it")
	case 1:
		region := strings.Join(lines[at[0]:at[0]+len(want)], "")
		if len(region) > editHintBytes {
			return fmt.Errorf("%s; ignoring whitespace it matches lines %d-%d, which differ from old in spacing: read them with fs-read and copy them exactly", base, at[0]+1, at[0]+len(want))
		}
		return fmt.Errorf("%s; ignoring whitespace it matches lines %d-%d, which differ from old in spacing. Those lines are exactly:\n%s", base, at[0]+1, at[0]+len(want), region)
	default:
		return fmt.Errorf("%s; ignoring whitespace it matches %d places (first at line %d): copy the exact text of the one you mean, with more context", base, len(at), at[0]+1)
	}
}

// normalizedLines is old's lines with whitespace normalized, leading and
// trailing blank lines dropped.
func normalizedLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		out = append(out, normalizeLine(l))
	}
	for len(out) > 0 && out[0] == "" {
		out = out[1:]
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}

// normalizeLine drops a line's leading and trailing whitespace and its
// ending, and collapses runs of spaces and tabs inside it.
func normalizeLine(l string) string {
	return strings.Join(strings.Fields(l), " ")
}

func fsEdit(o Options, pin guard.RepoPin) *scheme.Prim {
	preview := func(args []scheme.Value) string {
		if len(args) < 3 || args[0].Kind != scheme.KString || args[1].Kind != scheme.KString || args[2].Kind != scheme.KString {
			return ""
		}
		all := len(args) > 3 && scheme.Equal(args[3], scheme.True)
		abs := pin.Resolve(args[0].S)
		if abs == "" {
			return ""
		}
		cur, err := os.ReadFile(abs)
		if err != nil {
			return "cannot read " + args[0].S + ": " + err.Error()
		}
		next, err := applyEdit(string(cur), args[1].S, args[2].S, all)
		if err != nil {
			return "edit would fail: " + err.Error()
		}
		return Unified(args[0].S, string(cur), next, 3)
	}
	return &scheme.Prim{
		Name: "fs-edit",
		Meta: scheme.Meta{
			Doc: "Edit a text file inside the repository by replacing exactly one occurrence of old with new, or every occurrence with replace_all. old must match the file exactly (including whitespace) and, without replace_all, be unique; include surrounding lines to disambiguate. If old is not found but matches when whitespace is ignored, the error quotes the file's exact text. Prefer this over fs-write for changes to existing files.",
			Params: []scheme.Param{
				{Name: "path", Type: "string", Doc: "path relative to the repository root", Role: "path"},
				{Name: "old", Type: "string", Doc: "the exact text to replace (must occur exactly once, unless replace_all)"},
				{Name: "new", Type: "string", Doc: "the replacement text", Role: "payload"},
				{Name: "replace_all", Type: "boolean", Doc: "replace every occurrence of old instead of exactly one (default false)", Optional: true},
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
			all := len(c.Args) > 3 && scheme.Equal(c.Args[3], scheme.True)
			next, err := applyEdit(string(cur), old, repl, all)
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
