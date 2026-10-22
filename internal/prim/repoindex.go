package prim

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/fclaude/brote/scheme"
	"github.com/fclaude/nodwyn/internal/guard"
	nlog "github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/policyfile"
	"github.com/fclaude/nodwyn/internal/redact"
	"github.com/fclaude/nodwyn/internal/retrieval"
)

// The repository index (ADR 50): index-search's "repo" scope ranks chunks
// of the working tree's files by their words, and index-update builds it
// for the whole tree or adds a directory git ignores. What is stored is
// words and line numbers, never text, in nodwyn's data directory; every
// file enters through a guarded fs-read, redacted as the model would read
// it, and every result shown is read from the file again.
const (
	indexFileLimit = 20000     // files a tree's index holds
	indexTextLimit = 128 << 20 // and bytes of their text
	indexGitBytes  = 64 << 20  // what git ls-files may print
	// A search reads changed files for at most this long before it answers;
	// an index-update call reads for at most indexUpdateTime, and the next
	// call goes on from where it stopped.
	indexSearchTime = 10 * time.Second
	indexUpdateTime = 60 * time.Second
	// Stored chunks that no tree has named for this long are removed by
	// index-update, and by a search that read a file, at most this often.
	indexKeep         = 7 * 24 * time.Hour
	indexCollectEvery = time.Hour
	// Lines shown of each chunk found, and characters of each line.
	indexShown   = 3
	indexLineCut = 200
)

// repoIndex is one session's view of its working tree's index: the store
// it shares with every session of the project, and, in memory, the index
// built from the manifest it last read.
type repoIndex struct {
	o     Options
	pin   guard.RepoPin
	store retrieval.Store

	mu sync.Mutex
	// passed is the state of each file (by its path in the tree) as this
	// session found it searchable: a file in the same state has no need of
	// the path and denylist checks again, which resolve links and cost most
	// of a search of an unchanged tree. A session has one guard, so what
	// passed it stays passed; another session checks for itself.
	passed   map[string]retrieval.FileState
	manifest []byte // the manifest the index below was built from
	index    retrieval.Index
	chunks   []indexedChunk // by document number
	files    int
}

type indexedChunk struct {
	path       string
	start, end int
}

// refresh brings the tree's manifest up to date, reading through the
// guarded fs-read each file that changed or is new, for at most bound,
// and returns what it did and the manifest. add, a path relative to the
// root, is indexed whole from now on, git's ignores notwithstanding.
// Nobody is asked about a read (guard.WithoutAsking): a file the guard
// would ask about is left out and named, as one it refuses is.
func (x *repoIndex) refresh(c *scheme.Call, bound time.Duration, add string) (nlog.IndexUpdate, retrieval.Manifest, error) {
	start := time.Now()
	sum := nlog.IndexUpdate{Root: x.o.Root}
	read, ok := c.In.Lookup("fs-read")
	if !ok {
		return sum, retrieval.Manifest{}, errors.New("fs-read is unavailable")
	}
	deny := denylist(x.o)
	ctx := guard.WithoutAsking(c.Ctx)
	var m retrieval.Manifest
	// The manifest is published as a policy file is: one writer at a time
	// under a sibling lock, a temporary file renamed into place.
	err := policyfile.Update(c.Ctx, x.store.ManifestPath(x.o.Root), func(raw []byte) ([]byte, error) {
		m = retrieval.ParseManifest(raw, x.o.Root)
		before, err := m.Encode()
		if err != nil {
			return nil, err
		}
		if add != "" && !slices.Contains(m.Extra, add) {
			m.Extra = append(m.Extra, add)
			slices.Sort(m.Extra)
		}
		listed, err := x.list(ctx, m.Extra)
		if err != nil {
			return nil, err
		}
		if len(listed) > indexFileLimit {
			listed, sum.Partial = listed[:indexFileLimit], true
		}
		seen := map[string]bool{}
		drop := func(rel string) {
			if _, ok := m.Files[rel]; ok {
				delete(m.Files, rel)
				sum.Removed++
			}
		}
		var text int64
		for _, rel := range listed {
			seen[rel] = true
			abs := filepath.Join(x.o.Root, filepath.FromSlash(rel))
			info, err := os.Lstat(abs)
			searchable := err == nil
			if searchable && x.passed[rel] != stateOf(info, "") {
				if searchable = searchableFile(ctx, x.pin, deny, read, abs, x.o.MaxReadBytes); searchable {
					if x.passed == nil {
						x.passed = map[string]retrieval.FileState{}
					}
					x.passed[rel] = stateOf(info, "")
				}
			}
			if !searchable {
				delete(x.passed, rel)
				sum.Skipped++
				drop(rel)
				continue
			}
			if text+info.Size() > indexTextLimit {
				sum.Partial = true
				drop(rel)
				continue
			}
			if old, ok := m.Files[rel]; ok && sameFile(old, info) && x.store.Has(old.Hash) {
				text += info.Size()
				continue
			}
			if time.Since(start) > bound {
				sum.Unread++
				continue
			}
			value, err := c.In.Apply(ctx, scheme.Value{Kind: scheme.KPrim, Prim: read}, []scheme.Value{scheme.Str(abs)})
			var denied *scheme.Denied
			if errors.As(err, &denied) {
				sum.Refused = append(sum.Refused, rel)
				drop(rel)
				continue
			}
			// A file that cannot be read (its mode, its owner, gone since
			// it was listed) is skipped like a binary one: it used to fail
			// every search of the repository, and the model could not
			// leave it out.
			if err != nil && scheme.Catchable(ctx, err) {
				sum.Skipped++
				drop(rel)
				continue
			}
			if err != nil {
				return nil, err
			}
			if !utf8.ValidString(value.S) || strings.ContainsRune(value.S, 0) {
				sum.Skipped++
				drop(rel)
				continue
			}
			// Indexed as the model would read it: no credential shape
			// reaches the store.
			redacted := redact.Redact(value.S, nil).Text
			hash := retrieval.Hash(redacted)
			if x.store.Has(hash) {
				x.store.Touch(hash)
			} else if err := x.store.Put(hash, retrieval.Chunks(redacted)); err != nil {
				return nil, err
			}
			// The state is taken after the read, so a change made while
			// reading shows next time.
			if info, err = os.Lstat(abs); err != nil {
				drop(rel)
				continue
			}
			m.Files[rel] = stateOf(info, hash)
			sum.Read++
			text += info.Size()
		}
		for rel := range m.Files {
			if !seen[rel] {
				delete(m.Files, rel)
				sum.Removed++
			}
		}
		sum.Files = len(m.Files)
		after, err := m.Encode()
		if err != nil || bytes.Equal(before, after) {
			return nil, err
		}
		return after, nil
	})
	sum.Millis = time.Since(start).Milliseconds()
	return sum, m, err
}

// record keeps the summary of a build or refresh in the log (index.update),
// beside the guard's decision on each file it read.
func (x *repoIndex) record(sum nlog.IndexUpdate) {
	if x.o.IndexRecord != nil {
		x.o.IndexRecord(sum)
	}
}

// list is the tree's files, relative to the root and sorted: in a git
// work tree those git lists, tracked or untracked and not ignored (what
// git records as the project's, and the .nodwyn/ files only when they are
// tracked); otherwise the walk index-grep does. Each directory or file in
// extra is added whole.
func (x *repoIndex) list(ctx context.Context, extra []string) ([]string, error) {
	set := map[string]bool{}
	listed := false
	if isGitWorkTree(x.o.Root) {
		tracked, err1 := runGitLimit(ctx, x.o, indexGitBytes, "ls-files", "-z", "--cached")
		others, err2 := runGitLimit(ctx, x.o, indexGitBytes, "ls-files", "-z", "--others", "--exclude-standard")
		if err1 == nil && err2 == nil {
			for _, p := range strings.Split(tracked, "\x00") {
				set[p] = p != ""
			}
			for _, p := range strings.Split(others, "\x00") {
				if p != "" && !strings.HasPrefix(p, ".nodwyn/") {
					set[p] = true
				}
			}
			listed = true
		}
	}
	walk := func(dir string) error {
		files, err := searchFiles(ctx, x.o.Root, dir, "", 0)
		if err != nil {
			return err
		}
		for _, f := range files {
			if rel, err := filepath.Rel(x.o.Root, f); err == nil {
				set[filepath.ToSlash(rel)] = true
			}
		}
		return nil
	}
	if !listed {
		// Without git (or with git failing), the walk: hidden entries and
		// links skipped, no ignore file read.
		if err := walk(x.o.Root); err != nil {
			return nil, err
		}
	}
	for _, dir := range extra {
		if err := walk(filepath.Join(x.o.Root, filepath.FromSlash(dir))); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	var out []string
	for p, ok := range set {
		if ok {
			out = append(out, p)
		}
	}
	slices.Sort(out)
	return out, nil
}

func stateOf(info os.FileInfo, hash string) retrieval.FileState {
	s := retrieval.FileState{Size: info.Size(), MTime: info.ModTime().UnixNano(), Hash: hash}
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		s.Inode = st.Ino
	}
	return s
}

func sameFile(old retrieval.FileState, info os.FileInfo) bool {
	now := stateOf(info, old.Hash)
	return now == old
}

// load builds the in-memory index from the manifest, unless it was built
// from this one already. A chunk's path counts three times beside its
// words: a file's name says much of what is in it.
func (x *repoIndex) load(m retrieval.Manifest) error {
	raw, err := m.Encode()
	if err != nil || bytes.Equal(raw, x.manifest) {
		return err
	}
	x.index, x.chunks, x.files, x.manifest = retrieval.Index{}, nil, 0, raw
	paths := make([]string, 0, len(m.Files))
	for p := range m.Files {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	for _, p := range paths {
		chunks, ok := x.store.Chunks(m.Files[p].Hash)
		if !ok {
			continue // read again by the next refresh, which finds it missing
		}
		x.files++
		for _, ch := range chunks {
			words := make(map[string]int, len(ch.Words)+8)
			for w, n := range ch.Words {
				words[w] = n
			}
			retrieval.Count(words, p, 3)
			x.index.AddCounts(words)
			x.chunks = append(x.chunks, indexedChunk{path: p, start: ch.Start, end: ch.End})
		}
	}
	return nil
}

// search refreshes the index and returns the chunks that best match query,
// under (a path relative to the root, "" for all) if given, each read from
// the file now through the guarded fs-read: its heading line and the lines
// that hold the most of the words matched.
func (x *repoIndex) search(c *scheme.Call, query string, limit int, under string) (string, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	sum, m, err := x.refresh(c, indexSearchTime, "")
	if err != nil {
		return "", err
	}
	if sum.Read > 0 || sum.Removed > 0 {
		x.record(sum)
	}
	if sum.Read > 0 {
		// Every file edited leaves its old chunks behind, and a search is
		// all the use most sessions make of the index: it cleans up too, off
		// the manifest's lock (refresh has released it).
		_ = x.store.CollectDue(time.Now().Add(-indexKeep), indexCollectEvery)
	}
	if err := x.load(m); err != nil {
		return "", err
	}
	var keep func(int) bool
	if under != "" && under != "." {
		keep = func(doc int) bool {
			p := x.chunks[doc].path
			return p == under || strings.HasPrefix(p, under+"/")
		}
	}
	state := x.state(sum)
	hits := x.index.Search(query, limit, keep)
	if len(hits) == 0 {
		return fmt.Sprintf("No chunk of the %d files indexed holds any of these words.%s", x.files, state), nil
	}
	read, ok := c.In.Lookup("fs-read")
	if !ok {
		return "", errors.New("fs-read is unavailable")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "[the %d best of %d chunks in %d files, best first; read more with fs-read]%s\n", len(hits), len(x.chunks), x.files, state)
	for _, h := range hits {
		ch := x.chunks[h.Doc]
		abs := filepath.Join(x.o.Root, filepath.FromSlash(ch.path))
		// Read now, through the whole guard: what the index found is shown
		// only as the guard lets the file be read today.
		value, err := c.In.Apply(c.Ctx, scheme.Value{Kind: scheme.KPrim, Prim: read},
			[]scheme.Value{scheme.Str(abs), scheme.Int(int64(ch.start)), scheme.Int(int64(ch.end - ch.start + 1))})
		var denied *scheme.Denied
		if errors.As(err, &denied) {
			x.forget(c.Ctx, ch.path)
			return "", err
		}
		if err != nil {
			continue // the file changed since the refresh: a line past its end
		}
		lines := strings.Split(strings.TrimSuffix(value.S, "\n"), "\n")
		if len(lines) > 0 && strings.HasPrefix(lines[0], "[line") {
			lines = lines[1:]
		}
		if len(lines) == 0 {
			continue
		}
		fmt.Fprintf(&b, "%s:%d-%d: %s\n", ch.path, ch.start, ch.end, cutLine(lines[0]))
		for _, n := range bestOf(lines[1:], h.Matched) {
			fmt.Fprintf(&b, "  %d: %s\n", ch.start+1+n, cutLine(lines[1+n]))
		}
	}
	return b.String(), nil
}

// state says what a search should know of the index beyond its results.
func (x *repoIndex) state(sum nlog.IndexUpdate) string {
	var notes []string
	if sum.Unread > 0 {
		notes = append(notes, fmt.Sprintf("%d changed files were not read again in time: index-update reads them", sum.Unread))
	}
	if sum.Partial {
		notes = append(notes, fmt.Sprintf("the tree has more than %d files or %d MiB of text, so the index holds only part of it: search a directory with index-grep", indexFileLimit, indexTextLimit>>20))
	}
	if len(notes) == 0 {
		return ""
	}
	return " (" + strings.Join(notes, "; ") + ")"
}

// forget drops a file the guard now refuses from the tree's index.
func (x *repoIndex) forget(ctx context.Context, rel string) {
	_ = policyfile.Update(ctx, x.store.ManifestPath(x.o.Root), func(raw []byte) ([]byte, error) {
		m := retrieval.ParseManifest(raw, x.o.Root)
		if _, ok := m.Files[rel]; !ok {
			return nil, nil
		}
		delete(m.Files, rel)
		return m.Encode()
	})
	x.manifest = nil
	delete(x.passed, rel)
}

// update brings the index up to date, adding path (relative to the root)
// whole when given, and says what it holds.
func (x *repoIndex) update(c *scheme.Call, path string) (string, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	sum, m, err := x.refresh(c, indexUpdateTime, path)
	if err != nil {
		return "", err
	}
	x.record(sum)
	if err := x.load(m); err != nil {
		return "", err
	}
	_ = x.store.Collect(time.Now().Add(-indexKeep))
	var b strings.Builder
	fmt.Fprintf(&b, "The index holds %d files in %d chunks: %d read now, %d removed, %d skipped (not a regular file, too large, binary or protected).",
		x.files, len(x.chunks), sum.Read, sum.Removed, sum.Skipped)
	if len(sum.Refused) > 0 {
		fmt.Fprintf(&b, " The guard refused %d, left out: %s.", len(sum.Refused), strings.Join(sum.Refused, ", "))
	}
	if sum.Unread > 0 {
		fmt.Fprintf(&b, " %d files are still to read: call index-update again.", sum.Unread)
	}
	if sum.Partial {
		fmt.Fprintf(&b, " The tree has more than %d files or %d MiB of text, so the index holds only part of it.", indexFileLimit, indexTextLimit>>20)
	}
	if len(m.Extra) > 0 {
		fmt.Fprintf(&b, " Indexed whole besides what git lists: %s.", strings.Join(m.Extra, ", "))
	}
	return b.String(), nil
}

// bestOf are the numbers of the lines holding the most of words, at most
// indexShown of them, in their order.
func bestOf(lines []string, words []string) []int {
	type scored struct{ n, score int }
	var all []scored
	for i, line := range lines {
		score := 0
		has := retrieval.Words(line)
		for _, w := range words {
			if slices.Contains(has, w) {
				score++
			}
		}
		if score > 0 {
			all = append(all, scored{i, score})
		}
	}
	slices.SortStableFunc(all, func(p, q scored) int { return q.score - p.score })
	var out []int
	for _, s := range all[:min(indexShown, len(all))] {
		out = append(out, s.n)
	}
	slices.Sort(out)
	return out
}

func cutLine(s string) string {
	s = strings.TrimRight(s, "\r")
	if r := []rune(s); len(r) > indexLineCut {
		return string(r[:indexLineCut]) + "…"
	}
	return s
}

// indexPrims are index-search and index-update. index-search's other
// scope, the conversation, is the core's (Options.Conversation).
func indexPrims(o Options, pin guard.RepoPin) []*scheme.Prim {
	var x *repoIndex
	if o.IndexDir != "" {
		x = &repoIndex{o: o, pin: pin, store: retrieval.Store{Dir: o.IndexDir}}
	}
	relative := func(c *scheme.Call, i int) (string, error) {
		if len(c.Args) <= i || c.Args[i].S == "" {
			return "", nil
		}
		abs, err := resolve(pin, c, i)
		if err != nil {
			return "", err
		}
		rel, err := filepath.Rel(o.Root, abs)
		if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
			return "", fmt.Errorf("%s is outside the repository: the index covers the repository's files", c.Args[i].S)
		}
		if rel == "." {
			return "", nil
		}
		return filepath.ToSlash(rel), nil
	}
	search := &scheme.Prim{
		Name: "index-search",
		Meta: scheme.Meta{
			Doc: "Search by words, best match first. Scope \"repo\" searches the repository's files: for code by what it does or under names you can only guess, give plain words and likely names together, such as \"credential binding check egress\"; it reads the files that changed first. Each match is a file's lines A-B, its first line and its lines that hold the most of your words; read more with fs-read. For an exact string or a pattern use index-grep. Scope \"conversation\" searches this task's conversation: the person's messages, your replies and their tool calls, and tool results, including those the window has since elided, folded or cut, and sub-agents' work; each match is an event's seq with its best lines, and recall brings one back whole. Use it to find what was said, decided, run or shown earlier rather than run a command again. Scope \"earlier\" searches this project's other tasks the same way, when the person has turned that on for the project; each match names its task, and recall takes the seq with the task.",
			Params: []scheme.Param{
				{Name: "query", Type: "string", Doc: "words or names to look for: chunks and events holding more of them, and rarer ones, rank first; a name matches its parts too (ScrubEnv matches scrub)"},
				{Name: "scope", Type: "string", Doc: "\"repo\": the repository's files; \"conversation\": this task's conversation; \"earlier\": this project's earlier tasks, when the person allows it"},
				{Name: "limit", Type: "integer", Doc: "how many to return: 8 unless given, at most 20", Optional: true},
				{Name: "path", Type: "string", Doc: "with \"repo\", only this directory or file of the repository", Role: "path", Optional: true},
			},
			Host: true,
		},
		Handler: func(c *scheme.Call) (scheme.Value, error) {
			query, err := c.String(0)
			if err != nil {
				return scheme.Void, err
			}
			scope, err := c.String(1)
			if err != nil {
				return scheme.Void, err
			}
			if len(retrieval.Words(query)) == 0 {
				return scheme.Void, errors.New("the query has no words to look for")
			}
			limit := int64(searchDefault)
			if len(c.Args) > 2 {
				n, err := c.Int(2)
				if err != nil {
					return scheme.Void, err
				}
				if n < 0 {
					return scheme.Void, fmt.Errorf("limit must be positive, got %d", n)
				}
				if n > 0 {
					limit = min(n, searchMost)
				}
			}
			switch scope {
			case "repo":
				if x == nil {
					return scheme.Void, errors.New("this session keeps no repository index (it has no data directory)")
				}
				under, err := relative(c, 3)
				if err != nil {
					return scheme.Void, err
				}
				out, err := x.search(c, query, int(limit), under)
				if err != nil {
					return scheme.Void, err
				}
				return scheme.Tainted(scheme.Str(out), scheme.TaintOf(scheme.TagFile)), nil
			case "conversation", "earlier":
				if len(c.Args) > 3 && c.Args[3].S != "" {
					return scheme.Void, errors.New("path narrows a search of the repository, not of a conversation")
				}
				if o.Conversation == nil {
					return scheme.Void, errors.New("this session has no conversation to search")
				}
				out, err := o.Conversation(c.Ctx, scope, query, int(limit))
				if err != nil {
					return scheme.Void, err
				}
				// The log holds file contents and command output.
				return scheme.Tainted(scheme.Str(out), scheme.TaintOf("external:log")), nil
			}
			return scheme.Void, fmt.Errorf("scope %q: the scope is \"repo\", the repository's files, \"conversation\", this task's conversation, or \"earlier\", this project's earlier tasks", scope)
		},
	}
	update := &scheme.Prim{
		Name: "index-update",
		Meta: scheme.Meta{
			Doc: "Bring the repository's index up to date and say what it holds: the files index-search with scope \"repo\" ranks, which are those git lists (tracked, or untracked and not ignored). A search keeps the index up to date itself; call this to index the whole repository at once, to finish when a search said files were left to read, or with path to add a directory or file git ignores (a vendored library, generated code), which is then indexed whole from now on.",
			Params: []scheme.Param{
				{Name: "path", Type: "string", Doc: "a directory or file of the repository to index whole, git's ignores notwithstanding; the whole repository unless given", Role: "path", Optional: true},
			},
			Host: true, TaintSource: scheme.TagFile,
		},
		Handler: func(c *scheme.Call) (scheme.Value, error) {
			if x == nil {
				return scheme.Void, errors.New("this session keeps no repository index (it has no data directory)")
			}
			add, err := relative(c, 0)
			if err != nil {
				return scheme.Void, err
			}
			out, err := x.update(c, add)
			if err != nil {
				return scheme.Void, err
			}
			return scheme.Str(out), nil
		},
	}
	var out []*scheme.Prim
	if x != nil || o.Conversation != nil {
		out = append(out, search)
	}
	if x != nil {
		out = append(out, update)
	}
	return out
}

// searchDefault and searchMost bound what index-search returns.
const (
	searchDefault = 8
	searchMost    = 20
)
