package prim

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/fclaude/nodwyn/internal/guard"
	"github.com/fclaude/nodwyn/internal/scheme"
)

// A change often belongs in more than one place. django-16938 needs the same
// change at django/core/serializers/xml_serializer.py:158 as at python.py,
// and every failing session edited one of the two and never saw the other.
// After a successful edit of some size, fs-edit says where else in the
// repository the text it replaced occurs, as locations only: it does not say
// to edit them, since the same text is not always the same fault (ADR 32,
// amended 2026-10-17).
//
// It was right once in eight in the lean sessions' transcripts: the misses
// were import lines and decorators, generated and vendored copies, and a text
// that was everywhere. So an old that is only such lines is not searched, only
// the files git tracks are, outside build, dist, vendor and node_modules, and a
// text found in more than alsoShown other places gets no note at all.

// The scan is bounded in files and time, and a repository too large for it
// is searched as far as it goes. The names are variables so a test can lower
// them.
var (
	alsoFileLimit = 5000
	alsoTimeLimit = 400 * time.Millisecond
)

const (
	alsoMaxFile   = 1 << 20 // larger files are skipped
	alsoShown     = 3       // the most other places a note names; more is no note
	alsoMaxEdits  = 3       // edits of one call that get a note
	alsoMinLines  = 2       // non-blank lines of old, or
	alsoMinSpaces = 40      // non-space characters of it, for it to be worth a search
)

// worthSearching is whether old is long enough for another copy of it to
// mean something: a line or a few words occur everywhere, and so do import
// lines and decorators, however long.
func worthSearching(old string) bool {
	lines, chars, plain := 0, 0, false
	for _, l := range strings.Split(old, "\n") {
		n := len(strings.Join(strings.Fields(l), ""))
		if n > 0 {
			lines++
			plain = plain || !importOrDecorator(l)
		}
		chars += n
	}
	return plain && (lines >= alsoMinLines || chars >= alsoMinSpaces)
}

// importOrDecorator is whether a line is an import (import x, from x import
// y, import ... from '...') or a decorator.
func importOrDecorator(line string) bool {
	t := strings.TrimSpace(line)
	return strings.HasPrefix(t, "import ") || strings.HasPrefix(t, "@") ||
		(strings.HasPrefix(t, "from ") && strings.Contains(t, " import "))
}

// alsoSkipped are the folders whose files are no part of the source a change
// belongs in, though git may track them.
var alsoSkipped = []string{"build", "dist", "vendor", "node_modules"}

// alsoQuery is one edit's old text to look for elsewhere.
type alsoQuery struct {
	label string   // "edit 2: " in a list, "" for the one edit
	want  []string // old's lines, normalized (normalizedLines)
	token string   // the longest word of old, which any copy of it holds
	skip  []span   // where its new text lies in the edited file
	found []string // locations, path:line
	total int      // matches found; past alsoShown the scan stops for it
	done  bool
}

// matches are the lines of norm (a file's, normalized) at which the query's
// text begins: every line of want equals the file's, ignoring whitespace.
func (q *alsoQuery) matches(norm []string) []int {
	var at []int
	for i := 0; i+len(q.want) <= len(norm); i++ {
		if norm[i] != q.want[0] {
			continue
		}
		ok := true
		for k := 1; k < len(q.want); k++ {
			if norm[i+k] != q.want[k] {
				ok = false
				break
			}
		}
		if ok {
			at = append(at, i)
		}
	}
	return at
}

// alsoOccurs is the note fs-edit appends to its answer: for each edit that
// replaced a text worth searching for (and did not replace all of them),
// where else in the repository's tracked source the same text occurs, other
// places in the edited file included and the lines just edited left out, when
// that is one to alsoShown places. It says nothing when the scan finds none or
// more, when it runs out of files or time, or outside a git work tree, and
// never fails the edit.
func alsoOccurs(c *scheme.Call, o Options, pin guard.RepoPin, abs, shown, next string, edits []edit, list bool, sites [][]span) string {
	var queries []*alsoQuery
	for i, e := range edits {
		if e.all || len(queries) == alsoMaxEdits || !worthSearching(e.old) {
			continue
		}
		want := normalizedLines(e.old)
		if len(want) == 0 {
			continue
		}
		q := &alsoQuery{want: want, skip: sites[i]}
		for _, l := range want {
			for _, w := range strings.Fields(l) {
				if len(w) > len(q.token) {
					q.token = w
				}
			}
		}
		if list {
			q.label = fmt.Sprintf("edit %d: ", i+1)
		}
		queries = append(queries, q)
	}
	if len(queries) == 0 {
		return ""
	}
	ctx, cancel := context.WithTimeout(c.Ctx, alsoTimeLimit)
	defer cancel()
	s := &alsoScan{c: c, o: o, pin: pin, deny: denylist(o), ctx: ctx, queries: queries, seen: map[string]bool{}}
	s.read, _ = c.In.Lookup("fs-read")
	tracked, ok := s.tracked()
	rel, err := filepath.Rel(o.Root, abs)
	if !ok || err != nil || strings.HasPrefix(rel, "..") {
		return ""
	}
	rel = filepath.ToSlash(rel)
	// The edited file as it now is, then its folder, where a sibling is most
	// likely, then the rest of what git tracks, in its order.
	if tracked[rel] {
		s.seen[rel] = true
		s.files++
		s.search(abs, shown, []byte(next), true)
	}
	dir := filepath.ToSlash(filepath.Dir(rel))
	for _, pass := range []func(string) bool{
		func(f string) bool { return filepath.ToSlash(filepath.Dir(f)) == dir },
		func(string) bool { return true },
	} {
		for _, f := range s.order {
			if s.finished() {
				break
			}
			if pass(f) {
				s.candidate(f)
			}
		}
	}
	if s.exhausted {
		return ""
	}
	var lines []string
	for _, q := range queries {
		if len(q.found) == 0 || q.total > alsoShown {
			continue
		}
		lines = append(lines, fmt.Sprintf("%sthe replaced text also occurs at: %s; check whether the same change applies there", q.label, strings.Join(q.found, ", ")))
	}
	return strings.Join(lines, "\n")
}

// alsoScan is one search of the repository for the queries.
type alsoScan struct {
	c         *scheme.Call
	o         Options
	pin       guard.RepoPin
	deny      guard.PathDenylist
	read      *scheme.Prim
	ctx       context.Context
	queries   []*alsoQuery
	order     []string // the tracked source files, relative to the root, as git lists them
	seen      map[string]bool
	files     int
	exhausted bool // the file or time limit stopped the scan
}

// finished is whether the scan has nothing left to do: every query has its
// count, or the limits are spent.
func (s *alsoScan) finished() bool {
	if s.exhausted {
		return true
	}
	if s.files >= alsoFileLimit || s.ctx.Err() != nil {
		s.exhausted = true
		return true
	}
	for _, q := range s.queries {
		if !q.done {
			return false
		}
	}
	return true
}

// tracked lists the files git tracks that may hold source a change belongs in:
// source files (isSourceFile) outside hidden folders and alsoSkipped, in the
// order git lists them, and the whole set of what git tracks; ok is false
// outside a git work tree, or when git cannot say in time.
func (s *alsoScan) tracked() (map[string]bool, bool) {
	if !isGitWorkTree(s.o.Root) {
		return nil, false
	}
	out, err := runGitLimit(s.ctx, s.o, 32<<20, "ls-files", "-z")
	if err != nil {
		return nil, false
	}
	all := map[string]bool{}
	for _, f := range strings.Split(out, "\x00") {
		if f == "" {
			continue
		}
		all[f] = true
		parts := strings.Split(f, "/")
		skip := !isSourceFile(f)
		for _, part := range parts {
			skip = skip || strings.HasPrefix(part, ".") || slices.Contains(alsoSkipped, part)
		}
		if !skip {
			s.order = append(s.order, f)
		}
	}
	return all, true
}

// candidate searches the tracked file rel if it is a regular file of a size
// worth reading, not binary.
func (s *alsoScan) candidate(rel string) {
	if s.seen[rel] {
		return
	}
	s.seen[rel] = true
	s.files++
	path := filepath.Join(s.o.Root, filepath.FromSlash(rel))
	if info, err := os.Lstat(path); err != nil || !info.Mode().IsRegular() || info.Size() > alsoMaxFile {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	s.search(path, rel, data, false)
}

// search looks for the queries in one file's text, and notes where they
// occur. own says it is the edited file, in which the new text of the edit is
// left out.
func (s *alsoScan) search(path, shown string, data []byte, own bool) {
	if bytes.IndexByte(data[:min(len(data), 8192)], 0) >= 0 || !utf8.Valid(data) {
		return
	}
	var norm, raw []string
	checked, allowed := false, false
	for _, q := range s.queries {
		if q.done || (q.token != "" && !bytes.Contains(data, []byte(q.token))) {
			continue
		}
		if norm == nil {
			raw = strings.SplitAfter(string(data), "\n")
			norm = make([]string, len(raw))
			for i, l := range raw {
				norm[i] = normalizeLine(l)
			}
		}
		for _, i := range q.matches(norm) {
			if own && q.hits(raw, i) {
				continue
			}
			// A file the guard would not let the model read does not give
			// away what it holds, not even that it holds this: the nested
			// read is the one the model's own would be (index-grep's), and
			// asked once, and only for a file that matches.
			if !checked {
				checked, allowed = true, own || s.mayRead(path)
			}
			if !allowed {
				return
			}
			q.total++
			if len(q.found) < alsoShown {
				q.found = append(q.found, fmt.Sprintf("%s:%d", shown, i+1))
			}
			if q.total > alsoShown {
				q.done = true // too many to name: no note
				break
			}
		}
	}
}

// hits is whether the match of the query at line i of raw is its own new
// text, which the edit has just written.
func (q *alsoQuery) hits(raw []string, i int) bool {
	if len(q.skip) == 0 {
		return false
	}
	from := 0
	for _, l := range raw[:i] {
		from += len(l)
	}
	to := from
	for _, l := range raw[i : i+len(q.want)] {
		to += len(l)
	}
	for _, sk := range q.skip {
		if from < sk.to && to > sk.from {
			return true
		}
	}
	return false
}

// mayRead is whether the model may read the file at path: inside the
// repository, not on the denylist, and read through the guard as a read of its
// own would be. (The edited file is not asked about: the model has just edited
// it.)
func (s *alsoScan) mayRead(path string) bool {
	if len(s.queries) == 0 || s.read == nil {
		return false
	}
	if !searchableFile(s.ctx, s.pin, s.deny, s.read, path, alsoMaxFile) {
		return false
	}
	_, err := s.c.In.Apply(s.ctx, scheme.Value{Kind: scheme.KPrim, Prim: s.read}, []scheme.Value{scheme.Str(path)})
	return err == nil
}
