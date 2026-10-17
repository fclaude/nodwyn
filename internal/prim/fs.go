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

	"github.com/fclaude/nodwyn/internal/guard"
	"github.com/fclaude/nodwyn/internal/redact"
	"github.com/fclaude/nodwyn/internal/scheme"
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
	doc := "Read a UTF-8 text file inside the repository. Without start, returns the whole file. With start (a 1-based line number) and count (default 200), returns a header [lines A-B of N] and those lines exactly as in the file, without line numbers. Read files over about 18 KB in ranges: a whole read that large is shortened in your context. index-grep gives line numbers to start from."
	params := []scheme.Param{
		{Name: "path", Type: "string", Doc: "path relative to the repository root", Role: "path"},
		{Name: "start", Type: "integer", Doc: "first line to read, 1-based; omit both start and count to read the whole file", Optional: true},
		{Name: "count", Type: "integer", Doc: "how many lines to read from start (default 200)", Optional: true},
	}
	if o.Lean {
		// The lean profile's read by definition (lean6): the fourth parameter
		// is offered only there, so the tool a full session is shown is the
		// one it always had.
		doc += " With symbol (a definition named in outline, such as Class.method or a function), returns that definition's lines under the header, and start and count are ignored."
		params = append(params, scheme.Param{Name: "symbol", Type: "string", Doc: "read one definition instead of a range: Class.method, a function or a type, as outline lists it", Optional: true})
	}
	return &scheme.Prim{
		Name: "fs-read",
		Meta: scheme.Meta{
			Doc:         doc,
			Params:      params,
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
			if len(c.Args) > 3 && c.Args[3].Kind == scheme.KString && strings.TrimSpace(c.Args[3].S) != "" {
				syms, _, _, err := outlineSymbols(c.Ctx, o, abs)
				if err != nil {
					return scheme.Value{}, fmt.Errorf("symbol %q: %w", c.Args[3].S, err)
				}
				sym, err := findSymbol(syms, c.Args[3].S)
				if err != nil {
					return scheme.Value{}, err
				}
				budget := o.MaxRangeBytes
				if o.RangeBytes != nil {
					budget = o.RangeBytes()
				}
				text, err := readLines(abs, c.Args[0].S, int64(sym.Line), int64(max(1, sym.End-sym.Line+1)), budget)
				if err != nil {
					return scheme.Value{}, err
				}
				head := sym.Kind + " " + sym.qualified()
				if sym.Scope != "" {
					head += " (in " + sym.Scope + ")"
				}
				return scheme.Str(head + "\n" + text), nil
			}
			if len(c.Args) > 1 && !(len(c.Args) > 3 && c.Args[1].Kind == scheme.KInt && c.Args[1].I == 0 && c.Args[2].Kind == scheme.KInt && c.Args[2].I == 0) {
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
				budget := o.MaxRangeBytes
				if o.RangeBytes != nil {
					budget = o.RangeBytes()
				}
				text, err := readLines(abs, c.Args[0].S, start, count, budget)
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
// its line ending included, so a copy of it matches for fs-edit (ReadLines).
// The file is streamed, so its size is not limited.
func readLines(abs, name string, start, count int64, budget int) (string, error) {
	f, err := os.Open(abs)
	if err != nil {
		return "", err
	}
	defer f.Close()
	return ReadLines(f, name, start, count, budget)
}

// ReadLines returns lines start..start+count-1 of what src holds (shown as
// name) under a header "[lines A-B of N]", each line exactly as it is
// there. Output stops at a whole line once it would pass budget bytes, and
// the header's B says where it stopped; a first line longer than budget is
// cut and the header says so. A ranged fs-read reads a file with it, and
// recall an event of the log, so both page alike.
func ReadLines(src io.Reader, name string, start, count int64, budget int) (string, error) {
	if start == 0 {
		start = 1 // start skipped (count given alone) or 0: from the top
	}
	if start < 1 {
		return "", fmt.Errorf("start is a 1-based line number, got %d", start)
	}
	if count < 1 {
		return "", fmt.Errorf("count must be at least 1, got %d", count)
	}
	r := bufio.NewReaderSize(src, 64<<10)
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
			var before []byte // absent for a new file, and read only to say what the write did
			if o.Lean {
				before, _ = os.ReadFile(abs)
			}
			if err := o.Versions.Keep(abs); err != nil {
				return scheme.Value{}, fmt.Errorf("keeping the previous version failed, so nothing was written: %w", err)
			}
			if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
				return scheme.Value{}, err
			}
			if !o.Lean {
				return scheme.Void, nil // "ok", as it answered before the lean profile
			}
			return scheme.Str(writeAnswer(c.Args[0].S, string(before), content, len(before) > 0)), nil
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
	// for one and the guard merges the reasons. Only a write names what it
	// changes: another integration's preview is its arguments.
	reason := ""
	if path, ok := guard.WritePath(p, args); ok {
		reason = "changes " + path
	}
	return guard.Decision{Verdict: guard.Confirm, Reason: reason, Detail: detail}
}

// errTooLargeToEdit is why readEditable refuses a file.
var errTooLargeToEdit = errors.New("too large to edit or replace here")

// readEditable reads a file fs-edit or an fs-write preview works on. Files
// past maxVersionFile are refused, as no version of one can be kept: a 500 MB
// log was read whole and scanned for credentials several times over.
func readEditable(abs string) ([]byte, error) {
	if st, err := os.Stat(abs); err == nil && st.Size() > maxVersionFile {
		return nil, fmt.Errorf("%s is %s: %w (at most %s)", filepath.Base(abs), byteCount(int(st.Size())), errTooLargeToEdit, byteCount(maxVersionFile))
	}
	return os.ReadFile(abs)
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
	old, err := readEditable(abs)
	newText := args[1].S
	switch {
	case errors.Is(err, errTooLargeToEdit):
		return fmt.Sprintf("%s: the file is too large to show a diff (%v)", args[0].S, err)
	case err != nil:
		return fmt.Sprintf("new file %s (%d lines, %d bytes)\n%s", args[0].S, strings.Count(newText, "\n"), len(newText), Unified(args[0].S, "", newText, 3))
	case string(old) == newText:
		return fmt.Sprintf("%s: contents unchanged", args[0].S)
	}
	return Unified(args[0].S, string(old), newText, 3)
}

// applyEdit computes the result of an fs-edit without writing it: exactly
// one occurrence of old must exist, or with all at least one, and every
// occurrence is replaced. A file that holds credentials is matched as the
// model reads it, with each credential a placeholder (ADR 43); old that is
// not there is looked for in the file's own text, as before.
func applyEdit(content, old, replacement string, all bool) (string, error) {
	next, _, _, err := applyEditAt(content, edit{old: old, new: replacement, all: all}, false)
	return next, err
}

// splice is one replacement made in a text: del bytes at offset at became
// ins bytes. The offsets are those of the text before the replacement.
type splice struct{ at, del, ins int }

// applyEditAt is applyEdit for one edit, and says what it replaced, so the
// caller knows where the new text lies. lean says the session is a lean one
// (ADR 32, amended 2026-10-17): its errors name no tool, and an old that is
// nowhere but held literal escape sequences (\n where a newline was meant)
// is matched unescaped when that matches exactly once (unescapedEdit), and
// note says so; a full session's edits and errors are the ones they were.
func applyEditAt(content string, e edit, lean bool) (next string, sp []splice, note string, err error) {
	if e.old == "" {
		return "", nil, "", errors.New("old text must not be empty")
	}
	v := newView(content)
	if len(v.subs) > 0 {
		if next, sp, found, err := v.apply(e.old, e.new, e.all, lean); found {
			return next, sp, "", err
		}
	}
	starts := occurrences(content, e.old)
	switch n := len(starts); {
	case n == 0:
		if lean {
			if next, sp, note, ok := unescapedEdit(content, e); ok {
				return next, sp, note, nil
			}
		}
		return "", nil, "", notFound(v.text, e.old, lean)
	case e.all:
		var b strings.Builder
		last := 0
		for _, at := range starts {
			b.WriteString(content[last:at])
			b.WriteString(e.new)
			sp = append(sp, splice{at: at, del: len(e.old), ins: len(e.new)})
			last = at + len(e.old)
		}
		b.WriteString(content[last:])
		return b.String(), sp, "", nil
	case n == 1:
		return content[:starts[0]] + e.new + content[starts[0]+len(e.old):], []splice{{at: starts[0], del: len(e.old), ins: len(e.new)}}, "", nil
	}
	return "", nil, "", ambiguous(content, starts)
}

// occurrences are the offsets at which old occurs in text, without
// overlapping, as strings.Count counts them and ReplaceAll replaces them.
func occurrences(text, old string) []int {
	var out []int
	for from := 0; from <= len(text); {
		i := strings.Index(text[from:], old)
		if i < 0 {
			break
		}
		out = append(out, from+i)
		from += i + max(1, len(old))
	}
	return out
}

// ambiguous is the error for an old text that occurs at the offsets in
// starts: where, with the line each begins on, so the next try copies the
// lines around the one it means (or passes replace_all) instead of reading
// the file again.
func ambiguous(text string, starts []int) error {
	const shown = 6
	var where []string
	for _, at := range starts[:min(shown, len(starts))] {
		line := 1 + strings.Count(text[:at], "\n")
		start := strings.LastIndex(text[:at], "\n") + 1
		first := strings.TrimSpace(text[start:min(len(text), start+160)])
		if nl := strings.IndexByte(first, '\n'); nl >= 0 {
			first = first[:nl]
		}
		where = append(where, fmt.Sprintf("line %d: %s", line, first))
	}
	more := ""
	if len(starts) > len(where) {
		more = fmt.Sprintf("; and %d more", len(starts)-len(where))
	}
	return fmt.Errorf("old text occurs %d times (%s%s); include more context so it is unique (a line before or after the one you mean), or pass replace_all to replace every one", len(starts), strings.Join(where, "; "), more)
}

// editHintBytes bounds the file text a not-found error quotes.
const editHintBytes = 2000

// notFound explains a failed match in content, the text the model reads
// (the file with its credentials redacted), so what it quotes is safe to
// show. When old matches exactly one place
// once whitespace is ignored (trailing spaces, tabs against spaces,
// indentation, CRLF), the error names those lines and quotes them exactly,
// so the next try copies the file's own text instead of guessing again.
//
// In a lean session (ADR 32, amended 2026-10-17) a miss shows the closest
// text instead of advising a tool the session may not have; a full session's
// advice is the one it had.
func notFound(content, old string, lean bool) error {
	const base = "old text not found; it must match exactly, including whitespace"
	if hint := credentialHint(old); hint != "" {
		return errors.New(base + ". " + hint)
	}
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
		// A longer old that matches for its first lines and then differs: say
		// where it stops matching, with the file's line and old's side by
		// side, since the next try is the one line that was wrong (a dropped
		// blank line, a changed word) and not a new read of the region.
		best, bestAt := 0, -1
		for i := range lines {
			k := 0
			for k < len(want) && i+k < len(lines) && normalizeLine(lines[i+k]) == want[k] {
				k++
			}
			if k > best {
				best, bestAt = k, i
			}
		}
		if len(want) > 1 && best >= 1 && best < len(want) && bestAt+best < len(lines) {
			cut := func(x string) string {
				x = strings.TrimSpace(x)
				if len(x) > 140 {
					x = x[:140] + "…"
				}
				return x
			}
			return fmt.Errorf("%s; the first %d lines of old match lines %d-%d (ignoring whitespace), then old line %d is `%s` where the file has `%s` (line %d)", base, best, bestAt+1, bestAt+best, best+1, cut(want[best]), cut(lines[bestAt+best]), bestAt+best+1)
		}
		if lean {
			return errors.New(base + "; " + closestRegion(lines, want))
		}
		return errors.New(base + "; read the region again with fs-read (start, count) and copy it")
	case 1:
		region := strings.Join(lines[at[0]:at[0]+len(want)], "")
		if len(region) > editHintBytes {
			if lean {
				return fmt.Errorf("%s; ignoring whitespace it matches lines %d-%d, which differ from old in spacing: look at them again and copy them exactly", base, at[0]+1, at[0]+len(want))
			}
			return fmt.Errorf("%s; ignoring whitespace it matches lines %d-%d, which differ from old in spacing: read them with fs-read and copy them exactly", base, at[0]+1, at[0]+len(want))
		}
		return fmt.Errorf("%s; ignoring whitespace it matches lines %d-%d, which differ from old in spacing. Those lines are exactly:\n%s", base, at[0]+1, at[0]+len(want), region)
	default:
		return fmt.Errorf("%s; ignoring whitespace it matches %d places (first at line %d): copy the exact text of the one you mean, with more context", base, len(at), at[0]+1)
	}
}

// credentialHint explains an old that holds credential-shaped text: the
// model is never shown the file's own, so what it typed there is a guess, and
// the placeholder it reads in its place is what matches (ADR 43).
func credentialHint(old string) string {
	if len(redact.Redact(old, nil).Events) == 0 {
		return ""
	}
	return "old contains credential-shaped text. nodwyn never shows you the file's own: it reads as a [REDACTED <class>] placeholder, " +
		"so what old holds there can only be a guess. Write the placeholder where the credential stands to match whatever the file holds there, " +
		"and in new to keep it"
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

// edit is one replacement in an fs-edit call.
type edit struct {
	old, new string
	all      bool
}

// editsOf reads an fs-edit call's arguments. (path old new [replace_all])
// is one edit; (path "" "" #f edits) is a list of them, applied in order,
// which is what a call naming only path and edits becomes (ArgsFromJSON
// gives skipped options their empty values). list reports the second
// form. Exactly one form must be given.
func editsOf(args []scheme.Value) (edits []edit, list bool, err error) {
	if len(args) < 5 {
		switch {
		case len(args) < 2:
			return nil, false, errors.New("give old and new, or edits (a non-empty list of {old, new}): neither was given. For example {\"path\": \"a.py\", \"old\": \"x = 1\", \"new\": \"x = 2\"} or {\"path\": \"a.py\", \"edits\": [{\"old\": \"x = 1\", \"new\": \"x = 2\"}]}")
		case len(args) < 3:
			return nil, false, errors.New("new is missing: give old and new, or edits (a list of {old, new})")
		case args[1].Kind != scheme.KString || args[2].Kind != scheme.KString:
			return nil, false, fmt.Errorf("old and new must be strings, got %s and %s", scheme.Write(args[1]), scheme.Write(args[2]))
		}
		all := len(args) > 3 && scheme.Equal(args[3], scheme.True)
		return []edit{{old: args[1].S, new: args[2].S, all: all}}, false, nil
	}
	if !scheme.Equal(args[1], scheme.Str("")) || !scheme.Equal(args[2], scheme.Str("")) || scheme.Equal(args[3], scheme.True) {
		return nil, true, errors.New("give either old and new (with replace_all) or edits, not both; replace_all goes inside each edit")
	}
	items, ok := args[4].Slice()
	switch {
	case !ok:
		return nil, true, fmt.Errorf("edits must be a list of {old, new}, got %s", scheme.Write(args[4]))
	case len(items) == 0:
		return nil, true, errors.New("edits is empty: give at least one {old, new}")
	}
	for i, it := range items {
		e, err := editOf(it)
		if err != nil {
			return nil, true, fmt.Errorf("edit %d: %w", i+1, err)
		}
		edits = append(edits, e)
	}
	return edits, true, nil
}

// editOf reads one element of edits: an alist with string keys, as
// json-parse makes a JSON object. A field the guard would not read as the
// edit's (a symbol key, a repeated name) is an error, not ignored, so what
// is written is what the guard judged.
func editOf(v scheme.Value) (edit, error) {
	fields, ok := v.Slice()
	if !ok || len(fields) == 0 {
		return edit{}, fmt.Errorf("expected an object with old and new, got %s", scheme.Write(v))
	}
	var e edit
	seen := map[string]bool{}
	for _, f := range fields {
		if f.Kind != scheme.KPair || f.P.Car.Kind != scheme.KString {
			return edit{}, fmt.Errorf("expected an object with old and new, got %s", scheme.Write(v))
		}
		key, val := f.P.Car.S, f.P.Cdr
		if seen[key] {
			return edit{}, fmt.Errorf("%s is given twice", key)
		}
		seen[key] = true
		switch key {
		case "old", "new":
			if val.Kind != scheme.KString {
				return edit{}, fmt.Errorf("%s must be a string, got %s", key, scheme.Write(val))
			}
			if key == "old" {
				e.old = val.S
			} else {
				e.new = val.S
			}
		case "replace_all":
			switch {
			case scheme.Equal(val, scheme.True):
				e.all = true
			case scheme.Equal(val, scheme.False), scheme.Equal(val, scheme.Sym("null")):
			default:
				return edit{}, fmt.Errorf("replace_all must be true or false, got %s", scheme.Write(val))
			}
		default:
			return edit{}, fmt.Errorf("unknown field %q: an edit has old, new and replace_all", key)
		}
	}
	if !seen["old"] || !seen["new"] {
		return edit{}, errors.New("an edit needs both old and new")
	}
	return e, nil
}

// applyEdits runs applyEdit over edits in order, each on the text the one
// before left, so the file is written once or not at all. In the list form
// a failure names the edit.
func applyEdits(content string, edits []edit, list, lean bool) (string, error) {
	next, _, _, err := applyEditsAt(content, edits, list, lean)
	return next, err
}

// applyEditsAt is applyEdits that also says where the new text of each edit
// lies in the result, in the order of edits (nil for one with replace_all,
// which leaves no single place to compare with), and what applyEditAt noted
// of each, labelled with the edit in the list form.
func applyEditsAt(content string, edits []edit, list, lean bool) (string, [][]span, []string, error) {
	sites := make([][]span, len(edits))
	var notes []string
	for i, e := range edits {
		next, sp, note, err := applyEditAt(content, e, lean)
		switch {
		case err == nil:
			for j := 0; j < i; j++ {
				sites[j] = shiftSpans(sites[j], sp)
			}
			if !e.all {
				sites[i] = []span{{sp[0].at, sp[0].at + sp[0].ins}}
			}
			if note != "" && list {
				note = fmt.Sprintf("edit %d: %s", i+1, note)
			}
			if note != "" {
				notes = append(notes, note)
			}
			content = next
		case !list:
			return "", nil, nil, err
		case i == 0:
			return "", nil, nil, fmt.Errorf("edit 1 of %d failed, so nothing was written: %w", len(edits), err)
		default:
			return "", nil, nil, fmt.Errorf("edit %d of %d failed, so nothing was written (it is matched against the text the edits before it leave): %w", i+1, len(edits), err)
		}
	}
	return content, sites, notes, nil
}

// span is a stretch of a text, from offset from up to to.
type span struct{ from, to int }

// shiftSpans says where spans, of the text before the replacements in sp,
// lie after them: moved by what the replacements before them added or took
// away, and a span a replacement cuts into now ends where its new text does.
func shiftSpans(spans []span, sp []splice) []span {
	out := make([]span, 0, len(spans))
	for _, s := range spans {
		out = append(out, span{movedBy(sp, s.from, false), movedBy(sp, s.to, true)})
	}
	return out
}

// movedBy is where offset pos of a text lies once the replacements in sp,
// ascending and apart, are made; inside replaced text, at the start of its
// new text, or at its end for the end of a span.
func movedBy(sp []splice, pos int, end bool) int {
	shift := 0
	for _, r := range sp {
		switch {
		case pos >= r.at+r.del:
			shift += r.ins - r.del
		case pos > r.at:
			if end {
				return r.at + shift + r.ins
			}
			return r.at + shift
		default:
			return pos + shift
		}
	}
	return pos + shift
}

func fsEdit(o Options, pin guard.RepoPin) *scheme.Prim {
	preview := func(args []scheme.Value) string {
		if len(args) < 1 || args[0].Kind != scheme.KString {
			return ""
		}
		edits, list, err := editsOf(args)
		if err != nil {
			return "edit would fail: " + err.Error()
		}
		abs := pin.Resolve(args[0].S)
		if abs == "" {
			return ""
		}
		cur, err := readEditable(abs)
		if err != nil {
			return "cannot read " + args[0].S + ": " + err.Error()
		}
		next, err := applyEdits(string(cur), edits, list, o.Lean)
		if err != nil {
			return "edit would fail: " + err.Error()
		}
		return Unified(args[0].S, string(cur), next, 3)
	}
	// A lean session's tool shows the closest text for a miss, repairs an old
	// written with literal escape sequences and notes where the replaced text
	// also occurs; a full session's is the one it had (ADR 32, amended
	// 2026-10-17).
	doc := "Edit a text file inside the repository by replacing exactly one occurrence of old with new, or every occurrence with replace_all. old must match the file exactly (including whitespace) and, without replace_all, be unique; include surrounding lines to disambiguate. Several changes to one file: one call with edits, a list of {old, new, replace_all} given instead of old and new; they apply in order, each to the text the one before left, and nothing is written unless every one applies. If old is not found but matches when whitespace is ignored, the error quotes the file's exact text. A credential-shaped text in a file reads to you as [REDACTED <class>]: write that placeholder in old to match whatever the file holds there, and in new, in the same order, to keep it (leave it out of new to drop it). It answers with the changed lines as the file now reads them, numbered, with two lines around each, so the next edit near them needs no new read. Prefer this over fs-write for changes to existing files."
	params := []scheme.Param{
		{Name: "path", Type: "string", Doc: "path relative to the repository root", Role: "path"},
		{Name: "old", Type: "string", Doc: "the exact text to replace (must occur exactly once, unless replace_all); not with edits", Optional: true},
		{Name: "new", Type: "string", Doc: "the replacement text; not with edits", Role: "payload", Optional: true},
		{Name: "replace_all", Type: "boolean", Doc: "replace every occurrence of old instead of exactly one (default false)", Optional: true},
		{Name: "edits", Type: "list", Doc: "several changes to this file, in order, instead of old and new", Optional: true, Items: []scheme.Param{
			{Name: "old", Type: "string", Doc: "the exact text to replace (must occur exactly once, unless replace_all)"},
			{Name: "new", Type: "string", Doc: "the replacement text", Role: "payload"},
			{Name: "replace_all", Type: "boolean", Doc: "replace every occurrence of old instead of exactly one (default false)", Optional: true},
		}},
	}
	if o.Lean {
		doc = "Edit a text file inside the repository by replacing exactly one occurrence of old with new, or every occurrence with replace_all. old must match the file exactly (including whitespace) and, without replace_all, be unique; include surrounding lines to disambiguate. Several changes to one file: one call with edits, a list of {old, new, replace_all} given instead of old and new; they apply in order, each to the text the one before left, and nothing is written unless every one applies. If old is not found, the error shows the lines of the file closest to it, numbered, when some are close; if old matches when whitespace is ignored, the error quotes the file's exact text. An old written with literal \\n, \\t or \\\" sequences that matches nowhere as written, but once with them as real newlines, tabs and quotes, is applied so, and the answer says so. A credential-shaped text in a file reads to you as [REDACTED <class>]: write that placeholder in old to match whatever the file holds there, and in new, in the same order, to keep it (leave it out of new to drop it). It answers with the changed lines as the file now reads them, numbered, with two lines around each. When the replaced text (two lines or more, or 40 characters) also occurs in one to three other places of the repository's tracked source, the answer ends with where, as path:line."
	}
	return &scheme.Prim{
		Name: "fs-edit",
		Meta: scheme.Meta{
			Doc:          doc,
			Params:       params,
			Host:         true,
			Irreversible: true,
			Preview:      preview,
		},
		Handler: func(c *scheme.Call) (scheme.Value, error) {
			abs, err := resolve(pin, c, 0)
			if err != nil {
				return scheme.Value{}, err
			}
			edits, list, err := editsOf(c.Args)
			if err != nil {
				return scheme.Value{}, err
			}
			cur, err := readEditable(abs)
			if err != nil {
				return scheme.Value{}, err
			}
			next, sites, notes, err := applyEditsAt(string(cur), edits, list, o.Lean)
			if err != nil {
				return scheme.Value{}, err
			}
			st, _ := os.Stat(abs)
			mode := os.FileMode(0o644)
			if st != nil {
				mode = st.Mode().Perm()
			}
			if err := o.Versions.Keep(abs); err != nil {
				return scheme.Value{}, fmt.Errorf("keeping the previous version failed, so nothing was written: %w", err)
			}
			if err := os.WriteFile(abs, []byte(next), mode); err != nil {
				return scheme.Value{}, err
			}
			// The lines as the model reads them: a credential in them is its
			// placeholder, as in any read and in a failed edit's quote.
			answer := editedRegionsCounting(c.Args[0].S, redact.Redact(string(cur), nil).Text, redact.Redact(next, nil).Text, o.Lean)
			for _, note := range notes {
				answer += "\n" + note
			}
			if o.Lean {
				if also := alsoOccurs(c, o, pin, abs, c.Args[0].S, next, edits, list, sites); also != "" {
					answer += "\n" + also
				}
			}
			return scheme.Str(answer), nil
		},
	}
}

// writeAnswer is what fs-write answers: how many lines it wrote and how many
// the file had, with a warning when most of an existing file is gone. It
// answered "ok" until 2026-10-17; in one benchmark run a whole-file rewrite
// dropped 117 of 426 lines, and the ImportError it caused showed three calls
// later (ADR 32, amended 2026-10-17).
func writeAnswer(path, before, after string, existed bool) string {
	n := len(splitLines(after))
	if !existed {
		return fmt.Sprintf("wrote %s: a new file of %d lines", path, n)
	}
	m := len(splitLines(before))
	out := fmt.Sprintf("wrote %s: %d lines (it had %d)", path, n, m)
	if m >= 30 && n*10 < m*7 {
		out += fmt.Sprintf("; the file lost %d of its %d lines: fs-write replaces the whole file, so if you meant to change part of it, fs-edit does that", m-n, m)
	}
	return out
}
