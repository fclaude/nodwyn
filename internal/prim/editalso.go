package prim

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
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

// The scan is bounded in files and time, and a repository too large for it
// is searched as far as it goes. The names are variables so a test can lower
// them.
var (
	alsoFileLimit = 5000
	alsoTimeLimit = 400 * time.Millisecond
)

const (
	alsoMaxFile   = 1 << 20 // larger files are skipped
	alsoShown     = 3       // locations named
	alsoCountCap  = 20      // matches counted before the scan stops
	alsoMaxEdits  = 3       // edits of one call that get a note
	alsoMinLines  = 2       // non-blank lines of old, or
	alsoMinSpaces = 40      // non-space characters of it, for it to be worth a search
)

// worthSearching is whether old is long enough for another copy of it to
// mean something: a line or a few words occur everywhere.
func worthSearching(old string) bool {
	lines, chars := 0, 0
	for _, l := range strings.Split(old, "\n") {
		n := len(strings.Join(strings.Fields(l), ""))
		if n > 0 {
			lines++
		}
		chars += n
	}
	return lines >= alsoMinLines || chars >= alsoMinSpaces
}

// alsoQuery is one edit's old text to look for elsewhere.
type alsoQuery struct {
	label string   // "edit 2: " in a list, "" for the one edit
	want  []string // old's lines, normalized (normalizedLines)
	token string   // the longest word of old, which any copy of it holds
	skip  []span   // where its new text lies in the edited file
	found []string // locations, path:line
	total int      // matches found, the scan stopped at alsoCountCap
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
// where else in the repository's source the same text occurs, other places in
// the edited file included and the lines just edited left out. It says
// nothing when the scan finds nothing or runs out of files or time before it
// finds a thing, and never fails the edit.
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
	// The edited file as it now is, then its folder, where a sibling is most
	// likely, then the rest of the repository.
	s.seen[abs] = true
	s.files++
	s.search(abs, shown, []byte(next), true)
	if rel, err := filepath.Rel(o.Root, abs); err == nil && !strings.HasPrefix(rel, "..") {
		s.folder(filepath.Dir(abs))
		s.walk()
	}
	var lines []string
	for _, q := range queries {
		if len(q.found) == 0 {
			continue
		}
		more := ""
		switch rest := q.total - alsoShown; {
		case rest > 0 && (q.total >= alsoCountCap || s.exhausted):
			more = fmt.Sprintf(" (and %d or more)", rest)
		case rest > 0:
			more = fmt.Sprintf(" (and %d more)", rest)
		}
		lines = append(lines, fmt.Sprintf("%sthe replaced text also occurs at: %s%s; check whether the same change applies there", q.label, strings.Join(q.found[:min(alsoShown, len(q.found))], ", "), more))
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

// folder searches the source files directly in dir.
func (s *alsoScan) folder(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if s.finished() {
			return
		}
		s.candidate(filepath.Join(dir, e.Name()), e)
	}
}

// walk searches the repository's source files, but for hidden entries,
// vendored code and installed packages: filepath.WalkDir in path order, which
// searchFilesWith cannot cut short or keep out of a directory.
func (s *alsoScan) walk() {
	_ = filepath.WalkDir(s.o.Root, func(path string, d fs.DirEntry, err error) error {
		if s.finished() {
			return filepath.SkipAll
		}
		if err != nil {
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		if path != s.o.Root && strings.HasPrefix(name, ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if name == "vendor" || name == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		s.candidate(path, d)
		return nil
	})
}

// candidate searches the file at path if it is a source file of a size
// worth reading: not a link, not hidden, not binary.
func (s *alsoScan) candidate(path string, d fs.DirEntry) {
	name := d.Name()
	if strings.HasPrefix(name, ".") || d.IsDir() || !d.Type().IsRegular() || !isSourceFile(name) || s.seen[path] {
		return
	}
	s.seen[path] = true
	s.files++
	if info, err := d.Info(); err != nil || info.Size() > alsoMaxFile {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	rel, err := filepath.Rel(s.o.Root, path)
	if err != nil {
		return
	}
	s.search(path, filepath.ToSlash(rel), data, false)
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
				checked, allowed = true, s.mayRead(path)
			}
			if !allowed {
				return
			}
			q.total++
			if len(q.found) < alsoShown {
				q.found = append(q.found, fmt.Sprintf("%s:%d", shown, i+1))
			}
			if q.total >= alsoCountCap {
				q.done = true
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
// own would be. The edited file is the model's already.
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
