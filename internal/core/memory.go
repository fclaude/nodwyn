package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/memory"
	"github.com/fclaude/nodwyn/internal/redact"
	"github.com/fclaude/nodwyn/internal/scheme"
)

// A session's memory (ADR 51) is kept beside its log as <id>.mem: the
// entries memory.Folder made from the log up to the end of its last whole
// turn, with where that turn's line ends and the Folder's state there, so a
// later read folds in only the turns the log gained. Like the log's index
// (log_index.go) it is derived and revocable: checked against the log's line
// where it ends before use, made again from the log on any mismatch, and
// deleting it is always safe. Its entries are made from the log, which was
// redacted as it was written. It is written by whichever nodwyn process
// searches, in nodwyn's data directory, where the model's file tools cannot
// reach (the default denylist).

const memVersion = 1

type memFile struct {
	Version int `json:"version"`
	// The last event folded, at the end of a whole turn.
	Seq   uint64 `json:"seq"`
	Start int64  `json:"start"`
	End   int64  `json:"end"`
	Hash  string `json:"hash"`
	// State is the Folder's there.
	State   memory.State   `json:"state"`
	Entries []memory.Entry `json:"entries"`
}

func memPath(dir, id string) string { return filepath.Join(dir, id+".mem") }

// memCache keeps each session's entries while its log is unchanged, as
// infoCache keeps its listing.
var memCache struct {
	mu      sync.Mutex
	entries map[string]cachedMem
}

type cachedMem struct {
	key     infoKey
	entries []memory.Entry
}

// Memory is the session's memory, brought to its last whole turn.
func (s Store) Memory(id string) ([]memory.Entry, error) {
	path, err := s.path(id)
	if err != nil {
		return nil, err
	}
	stat, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	key := infoKey{stat.Size(), stat.ModTime()}
	memCache.mu.Lock()
	cached, ok := memCache.entries[path]
	memCache.mu.Unlock()
	if ok && cached.key == key {
		return cached.entries, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("log: %w", err)
	}
	defer f.Close()
	m, loaded := loadMem(memPath(s.Dir, id), f)
	if !loaded {
		m = memFile{Version: memVersion}
	}
	out, err := extendMem(f, id, m)
	if err != nil && loaded {
		// The log does not go on from where the file says it ends: made
		// again from the start, the log says whether it is the log that
		// is wrong.
		out, err = extendMem(f, id, memFile{Version: memVersion})
	}
	if err != nil {
		return nil, err
	}
	if out.Seq > m.Seq || len(out.Entries) != len(m.Entries) {
		// Best effort, as the log's index is written.
		_ = writeDerived(s.Dir, id, ".mem", out)
	}
	memCache.mu.Lock()
	if memCache.entries == nil {
		memCache.entries = map[string]cachedMem{}
	}
	memCache.entries[path] = cachedMem{key, out.Entries}
	memCache.mu.Unlock()
	return out.Entries, nil
}

// extendMem folds in the events of f after the ones m covers. It covers only
// whole turns: an unfinished turn's events are read again next time, and an
// entry made from them again has the same ID, which replaces it.
func extendMem(f *os.File, id string, m memFile) (memFile, error) {
	if _, err := f.Seek(m.End, io.SeekStart); err != nil {
		return memFile{}, err
	}
	folder := memory.Folder{Session: id, State: m.State}
	entries := slices.Clone(m.Entries)
	at := map[string]int{}
	for i, e := range entries {
		at[e.ID] = i
	}
	covered := m
	base, previous := m.End, int64(0)
	err := log.ReplayFrom(f, m.Seq+1, func(ev log.Event, line []byte, end int64) error {
		start := base + previous
		previous = end
		for _, e := range folder.Observe(ev) {
			if i, ok := at[e.ID]; ok {
				entries[i] = e
			} else {
				at[e.ID] = len(entries)
				entries = append(entries, e)
			}
		}
		if !folder.Open() {
			covered.Seq, covered.Start, covered.End, covered.Hash, covered.State = ev.Seq, start, base+end, lineHash(line), folder.State
		}
		return nil
	})
	if err != nil && !errors.Is(err, log.ErrTruncated) {
		return memFile{}, err
	}
	covered.Version, covered.Entries = memVersion, entries
	return covered, nil
}

// loadMem reads the memory file at path when it matches the log f.
func loadMem(path string, f *os.File) (memFile, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return memFile{}, false
	}
	var m memFile
	if json.Unmarshal(raw, &m) != nil || m.Version != memVersion || m.Seq == 0 || !lineMatches(f, m.Seq, m.Start, m.End, m.Hash) {
		return memFile{}, false
	}
	return m, true
}

// MemoryHit is an entry a search found, with its task's title.
type MemoryHit struct {
	memory.Entry
	Task    string
	Score   float64
	Snippet string
}

// projectSessions are the sessions of project, newest activity first, only
// the one named when only is set.
func (s Store) projectSessions(project, only string) ([]SessionInfo, error) {
	infos, err := s.List()
	if err != nil {
		return nil, err
	}
	var out []SessionInfo
	for _, info := range infos {
		if info.Project() == project && (only == "" || info.ID == only) {
			out = append(out, info)
		}
	}
	slices.SortFunc(out, func(a, b SessionInfo) int { return b.LastEvent.Compare(a.LastEvent) })
	return out, nil
}

// SearchMemory ranks the memory of project's sessions (only's, when set) by
// the words they share with query, best first, at most limit of them. An
// entry is ranked by its own text: with its task's title beside it, every
// turn of a task whose title matched ranked alike, a "commit and push" as
// high as the turn that did the work.
func (s Store) SearchMemory(project, only, query string, limit int) ([]MemoryHit, error) {
	sessions, err := s.projectSessions(project, only)
	if err != nil {
		return nil, err
	}
	var entries []memory.Entry
	var texts []string
	titles := map[string]string{}
	for _, info := range sessions {
		got, err := s.Memory(info.ID)
		if err != nil {
			return nil, fmt.Errorf("core: %s: %w", info.ID, err)
		}
		titles[info.ID] = info.Title()
		for _, e := range got {
			entries = append(entries, e)
			texts = append(texts, e.Text)
		}
	}
	var out []MemoryHit
	for _, h := range memory.NewIndex(texts).Search(query, limit) {
		e := entries[h.Doc]
		out = append(out, MemoryHit{Entry: e, Task: titles[e.Session], Score: h.Score, Snippet: redact.Redact(memory.Snippet(e.Text, query, 300), nil).Text})
	}
	return out, nil
}

// TextHit is a place in a log where the words searched for are, as written.
type TextHit struct {
	Session string
	Task    string
	Seq     uint64
	Kind    log.Kind
	When    time.Time
	Snippet string
	// Turn is the ID of the card of the turn the event is in, when there is
	// one.
	Turn string
}

// ScanMemory finds phrase, as written and in any case, in the messages,
// replies and tool output of project's sessions (only's, when set), newest
// first, at most limit places. Each log is read backwards from its end
// through its index's marks, so the latest is found first and a long log is
// read only as far as needed.
func (s Store) ScanMemory(project, only, phrase string, limit int) ([]TextHit, error) {
	needle := strings.ToLower(strings.TrimSpace(phrase))
	if needle == "" {
		return nil, errors.New("give the words to find")
	}
	sessions, err := s.projectSessions(project, only)
	if err != nil {
		return nil, err
	}
	var out []TextHit
	for _, info := range sessions {
		if len(out) >= limit {
			break
		}
		hits, err := s.scanLog(info.ID, needle, limit-len(out))
		if err != nil {
			return nil, fmt.Errorf("core: %s: %w", info.ID, err)
		}
		cards, err := s.Memory(info.ID)
		if err != nil {
			return nil, fmt.Errorf("core: %s: %w", info.ID, err)
		}
		for i := range hits {
			hits[i].Task = info.Title()
			for _, c := range cards {
				if c.Level == memory.LevelTurn && c.From <= hits[i].Seq && hits[i].Seq <= c.Through {
					hits[i].Turn = c.ID
					break
				}
			}
		}
		out = append(out, hits...)
	}
	return out, nil
}

// scanLog reads a session's log backwards, a mark's span at a time, for
// needle, which is lower-cased.
func (s Store) scanLog(id, needle string, limit int) ([]TextHit, error) {
	idx, _, err := s.index(id)
	if err != nil {
		return nil, err
	}
	path, err := s.path(id)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("log: %w", err)
	}
	defer f.Close()
	var out []TextHit
	for k := len(idx.Marks) - 1; k >= 0 && len(out) < limit; k-- {
		if _, err := f.Seek(idx.Marks[k], io.SeekStart); err != nil {
			return nil, err
		}
		first, last := uint64(k)*markEvery+1, uint64(k+1)*markEvery
		var span []log.Event
		err := log.ReplayFrom(f, first, func(ev log.Event, _ []byte, _ int64) error {
			if ev.Seq > last {
				return errPageFull
			}
			span = append(span, ev)
			return nil
		})
		if err != nil && !errors.Is(err, errPageFull) && !errors.Is(err, log.ErrTruncated) {
			return nil, err
		}
		for i := len(span) - 1; i >= 0 && len(out) < limit; i-- {
			text := eventText(span[i])
			at := strings.Index(strings.ToLower(text), needle)
			if at < 0 {
				continue
			}
			out = append(out, TextHit{Session: id, Seq: span[i].Seq, Kind: span[i].Kind, When: span[i].TS, Snippet: redact.Redact(around(text, at, len(needle), 300), nil).Text})
		}
	}
	return out, nil
}

// around is the part of text around the n bytes at at, at most width bytes,
// on one line.
func around(text string, at, n, width int) string {
	start := max(0, at-width/3)
	for start > 0 && (text[start]&0xC0) == 0x80 {
		start--
	}
	end := min(len(text), start+width)
	for end < len(text) && (text[end]&0xC0) == 0x80 {
		end++
	}
	out := strings.Join(strings.Fields(text[start:end]), " ")
	if start > 0 {
		out = "…" + out
	}
	if end < len(text) {
		out += "…"
	}
	return out
}

// What memory-read shows of a turn's events.
const (
	readCap      = 12000
	readMessage  = 2000
	readReply    = 1500
	readResult   = 600
	readCallArgs = 200
)

// ReadMemory is one entry of project's memory, whole: its text, and for a
// turn the turn's events from the log, cut to fit; for an episode, the turns
// it covers.
func (s Store) ReadMemory(project, id string) (string, error) {
	session, from, through, level, err := memory.ParseID(id)
	if err != nil {
		return "", err
	}
	info, err := s.Info(session)
	if err != nil {
		return "", fmt.Errorf("no task %s here", session)
	}
	if info.Project() != project {
		return "", fmt.Errorf("task %s belongs to another repository", session)
	}
	entries, err := s.Memory(session)
	if err != nil {
		return "", err
	}
	i := slices.IndexFunc(entries, func(e memory.Entry) bool { return e.ID == id })
	if i < 0 {
		return "", fmt.Errorf("no entry %s: search again, the task's memory may have changed", id)
	}
	e := entries[i]
	var b strings.Builder
	kind := map[int]string{memory.LevelTurn: "turn", memory.LevelEpisode: "episode"}[level]
	fmt.Fprintf(&b, "%s · %s · %s · seq %d–%d\n\n%s\n", info.Title(), kind, e.Start.UTC().Format("2006-01-02 15:04Z"), from, through, e.Text)
	switch level {
	case memory.LevelEpisode:
		b.WriteString("\nturns in it:\n")
		for _, c := range entries {
			if c.Level == memory.LevelTurn && c.From >= from && c.Through <= through {
				asked, _, _ := strings.Cut(c.Text, "\n")
				fmt.Fprintf(&b, "%s  %s\n", c.ID, strings.TrimPrefix(asked, "asked   "))
			}
		}
	case memory.LevelTurn:
		b.WriteString("\nthe turn's events:\n")
		after := from - 1
		for after < through {
			page, err := s.ReadLog(session, after, through, 1000)
			if err != nil {
				return "", err
			}
			for _, ev := range page.Events {
				line := turnLine(ev)
				if line == "" {
					continue
				}
				if b.Len()+len(line) > readCap {
					fmt.Fprintf(&b, "… cut here: %d more events, through seq %d\n", int(through-ev.Seq)+1, through)
					return b.String(), nil
				}
				b.WriteString(line)
			}
			if !page.More || page.Next == after {
				break
			}
			after = page.Next
		}
	}
	return b.String(), nil
}

// turnLine is how memory-read shows one of a turn's events, "" for one it
// leaves out.
func turnLine(ev log.Event) string {
	clipped := func(s string, n int) string {
		s = strings.TrimSpace(s)
		if len(s) > n {
			return around(s, 0, 0, n)
		}
		return s
	}
	switch ev.Kind {
	case log.KindUserMsg:
		return fmt.Sprintf("[%d] person: %s\n", ev.Seq, clipped(eventText(ev), readMessage))
	case log.KindHarnessMsg:
		return fmt.Sprintf("[%d] nodwyn: %s\n", ev.Seq, clipped(eventText(ev), readResult))
	case log.KindLLMResp:
		var p log.LLMResp
		if ev.Decode(&p) != nil {
			return ""
		}
		var b strings.Builder
		if strings.TrimSpace(p.Content) != "" {
			fmt.Fprintf(&b, "[%d] model: %s\n", ev.Seq, clipped(p.Content, readReply))
		}
		for _, c := range p.ToolCalls {
			fmt.Fprintf(&b, "[%d] call %s %s\n", ev.Seq, c.Name, clipped(string(c.Args), readCallArgs))
		}
		return b.String()
	case log.KindToolResult:
		var p log.ToolResult
		if ev.Decode(&p) != nil {
			return ""
		}
		if !p.OK {
			return fmt.Sprintf("[%d] %s failed: %s\n", ev.Seq, p.Name, clipped(p.Error, readResult))
		}
		return fmt.Sprintf("[%d] %s: %s\n", ev.Seq, p.Name, clipped(p.Value, readResult))
	case log.KindTurnEnd:
		var p log.TurnEnd
		if ev.Decode(&p) == nil {
			return fmt.Sprintf("[%d] turn ended: %s\n", ev.Seq, p.Outcome)
		}
	}
	return ""
}

// How many results a search gives the model.
const memoryResults = 8

// registerMemory adds memory-search and memory-read (ADR 51): read-only
// tools over the memory of this task and, when the person lets the model
// search earlier tasks for the project (ADR 50 §9), of the repository's
// other tasks. Other repositories' tasks are never visible, as for
// session-list. Results are tool output, carrying the log's provenance
// (external:log).
func (s *Session) registerMemory() error {
	store := Store{Dir: s.opts.Paths.SessionsDir}
	for _, p := range []*scheme.Prim{
		{
			Name: "memory-search",
			Meta: scheme.Meta{
				Doc: "Search what this task, or with scope \"project\" this repository's tasks, did before: a card for each turn (what was asked, the plan and its marks, files written, commands run and how they ended, errors, the answer's start) and the notes written when a conversation was compacted. Ranked by the words they share with query. Each result has an id for memory-read, its task, when, and a snippet. With exact true, the tasks' messages, replies and tool output are searched instead for query as written, newest first: for an error message or a name no card kept.",
				Params: []scheme.Param{
					{Name: "query", Type: "string", Doc: "words to search for, such as the names, files or errors involved"},
					{Name: "scope", Type: "string", Doc: "\"task\" for this task (the default), \"project\" for every task in this repository, when the person has turned on searching earlier tasks", Optional: true},
					{Name: "exact", Type: "boolean", Doc: "true to find query as written in the logs instead of ranking the cards (default false)", Optional: true},
				},
				Host:        true,
				Origin:      "builtin",
				TaintSource: "external:log",
			},
			Handler: func(c *scheme.Call) (scheme.Value, error) {
				query, err := c.String(0)
				if err != nil {
					return scheme.Void, err
				}
				if strings.TrimSpace(query) == "" {
					return scheme.Void, errors.New("give the words to search for")
				}
				// This task's own memory is its conversation, which the
				// model may always read back (ADR 50 §8); the other tasks'
				// are earlier tasks, off unless the person allows them.
				only := s.id
				if len(c.Args) > 1 {
					scope, err := c.String(1)
					if err != nil {
						return scheme.Void, err
					}
					switch scope {
					case "", "task":
					case "project":
						if err := s.earlierAllowed(); err != nil {
							return scheme.Void, err
						}
						only = ""
					default:
						return scheme.Void, fmt.Errorf("scope is \"task\" or \"project\", not %q", scope)
					}
				}
				exact := len(c.Args) > 2 && c.Args[2].IsTrue()
				if exact {
					hits, err := store.ScanMemory(s.project(), only, query, memoryResults)
					if err != nil {
						return scheme.Void, err
					}
					var out []scheme.Value
					for _, h := range hits {
						out = append(out, scheme.List(
							scheme.Cons(scheme.Sym("id"), scheme.Str(h.Turn)),
							scheme.Cons(scheme.Sym("task"), scheme.Str(h.Task)),
							scheme.Cons(scheme.Sym("when"), scheme.Str(h.When.UTC().Format(time.RFC3339))),
							scheme.Cons(scheme.Sym("seq"), scheme.Int(int64(h.Seq))),
							scheme.Cons(scheme.Sym("in"), scheme.Str(string(h.Kind))),
							scheme.Cons(scheme.Sym("text"), scheme.Str(h.Snippet)),
						))
					}
					return scheme.List(out...), nil
				}
				hits, err := store.SearchMemory(s.project(), only, query, memoryResults)
				if err != nil {
					return scheme.Void, err
				}
				var out []scheme.Value
				for _, h := range hits {
					level := map[int]string{memory.LevelTurn: "turn", memory.LevelEpisode: "episode"}[h.Level]
					out = append(out, scheme.List(
						scheme.Cons(scheme.Sym("id"), scheme.Str(h.ID)),
						scheme.Cons(scheme.Sym("level"), scheme.Str(level)),
						scheme.Cons(scheme.Sym("task"), scheme.Str(h.Task)),
						scheme.Cons(scheme.Sym("when"), scheme.Str(h.Start.UTC().Format(time.RFC3339))),
						scheme.Cons(scheme.Sym("text"), scheme.Str(h.Snippet)),
					))
				}
				return scheme.List(out...), nil
			},
		},
		{
			Name: "memory-read",
			Meta: scheme.Meta{
				Doc: "Read one result of memory-search by its id: the whole card or note, and for a turn its events from the log (the person's message, the model's replies and calls, tool results), each cut to fit; for an episode, the turns it covers.",
				Params: []scheme.Param{
					{Name: "id", Type: "string", Doc: "an id memory-search gave, such as m:20261015T050000Z-9c1e0000:1981-2007:1"},
				},
				Host:        true,
				Origin:      "builtin",
				TaintSource: "external:log",
			},
			Handler: func(c *scheme.Call) (scheme.Value, error) {
				id, err := c.String(0)
				if err != nil {
					return scheme.Void, err
				}
				if task, _, _, _, err := memory.ParseID(id); err == nil && task != s.id {
					if err := s.earlierAllowed(); err != nil {
						return scheme.Void, err
					}
				}
				text, err := store.ReadMemory(s.project(), id)
				if err != nil {
					return scheme.Void, err
				}
				return scheme.Str(text), nil
			},
		},
	} {
		if err := s.interp.Register(p); err != nil {
			return err
		}
	}
	return nil
}
