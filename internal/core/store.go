package core

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/redact"
)

// Store is a view of the session logs, not a second database.
type Store struct{ Dir string }

// SessionInfo summarizes durable history. InFlight means a turn had no
// recorded end; it does not claim that a process is currently alive.
type SessionInfo struct {
	ID           string
	Repo         string
	FirstMessage string
	Started      time.Time
	LastEvent    time.Time
	InFlight     bool
	Truncated    bool
	// Changes are the files the guard let this session write, in the order
	// first written, from guard.decision records (older logs record none).
	// An allowed write is not proof the write succeeded.
	Changes []string
	// Run is the last recorded run.state, nil for a session that never ran
	// as a supervised run.
	Run *log.RunState
	// Name is the latest name a person gave the session (session.title),
	// "" when it has none. Archived is the latest session.archive (ADR 39).
	Name     string
	Archived bool
	// Worktree is the git worktree made for the session, whose path is
	// Repo; nil for a session in the folder itself (ADR 34).
	Worktree *log.Worktree
}

// Title is what a person reads a session by: the name they gave it, else
// the first sentence of the first message when it is short, else its first
// words. A run's first message is the run's prompt around its task, so a
// run is read by the task.
func (i SessionInfo) Title() string {
	if i.Name != "" {
		return i.Name
	}
	text := i.FirstMessage
	if i.Run != nil && strings.TrimSpace(i.Run.Task) != "" {
		text = i.Run.Task
	}
	title, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	title = strings.TrimSpace(title)
	if end := strings.IndexAny(title, ".?!"); end > 0 && end < 90 && (end+1 == len(title) || title[end+1] == ' ') {
		title = title[:end+1]
		title = strings.TrimSuffix(title, ".")
	}
	if r := []rune(title); len(r) > 80 {
		cut := string(r[:80])
		if space := strings.LastIndex(cut, " "); space > 40 {
			cut = cut[:space]
		}
		title = strings.TrimRight(cut, " ,;:") + "…"
	}
	return title
}

// maxChanges bounds SessionInfo.Changes: a list, not an audit.
const maxChanges = 50

var sessionID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

func (s Store) path(id string) (string, error) {
	if !sessionID.MatchString(id) {
		return "", fmt.Errorf("core: invalid session id %q", id)
	}
	path := log.SessionPath(s.Dir, id)
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("core: session log is not a regular file: %s", path)
	}
	return path, nil
}

// read reads a whole session log: its listing and every event in it.
func (s Store) read(id string) (SessionInfo, []log.Event, error) {
	path, err := s.path(id)
	if err != nil {
		return SessionInfo{ID: id}, nil, err
	}
	events, err := log.ReadFile(path)
	truncated := errors.Is(err, log.ErrTruncated)
	if err != nil && !truncated {
		return SessionInfo{ID: id, Truncated: truncated}, nil, err
	}
	f := infoFold{Info: SessionInfo{ID: id}}
	for _, ev := range events {
		if err := f.observe(id, ev); err != nil {
			f.Info.Truncated = truncated
			return f.Info, nil, err
		}
	}
	info, err := f.result(id)
	info.Truncated = truncated
	if err != nil {
		return info, nil, err
	}
	return info, events, nil
}

// infoFold is a session's listing as the events of its log so far make it,
// taken one event at a time, so that the log's index can keep it and fold
// in only the events the log gained since (ADR 49 §10). Start says the
// log's session.start was seen, First its first user.msg.
type infoFold struct {
	Info  SessionInfo
	Start bool
	First bool
}

// observe folds one event in, refusing a log that is not one session's.
func (f *infoFold) observe(id string, ev log.Event) error {
	if ev.Session != id {
		return fmt.Errorf("core: mixed session ids in %s", id)
	}
	info := &f.Info
	if ev.Seq == 1 {
		info.Started = ev.TS
	}
	info.LastEvent = ev.TS
	switch ev.Kind {
	case log.KindSessionStart:
		if f.Start {
			return fmt.Errorf("core: duplicate session.start in %s", id)
		}
		var start log.SessionStart
		if err := ev.Decode(&start); err != nil {
			return err
		}
		if !filepath.IsAbs(start.Repo) {
			return fmt.Errorf("core: session %s has no absolute repository path", id)
		}
		f.Start = true
		info.Repo = config.CanonicalPath(start.Repo)
		info.Worktree = start.Worktree
		if w := info.Worktree; w != nil {
			w.Repository = config.CanonicalPath(w.Repository)
			w.Path = config.CanonicalPath(w.Path)
		}
	case log.KindGuardDecision:
		var d log.GuardDecision
		if ev.Decode(&d) == nil && d.Wrote() && !slices.Contains(info.Changes, d.Path) && len(info.Changes) < maxChanges {
			info.Changes = append(info.Changes, d.Path)
		}
	case log.KindRunState:
		var r log.RunState
		if ev.Decode(&r) == nil {
			r.Task = redact.Redact(r.Task, nil).Text
			info.Run = &r
		}
	case log.KindSessionTitle:
		var t log.SessionTitle
		if ev.Decode(&t) == nil {
			info.Name = redact.Redact(t.Title, nil).Text
		}
	case log.KindWorktreeRestore:
		// A worktree made again on a new branch starts from where its base
		// was then (restoreWorktree): its latest base is its base.
		var c log.WorktreeChange
		if ev.Decode(&c) == nil && info.Worktree != nil && config.CanonicalPath(c.Path) == info.Worktree.Path && c.BaseCommit != "" {
			w := *info.Worktree
			w.BaseCommit = c.BaseCommit
			info.Worktree = &w
		}
	case log.KindSessionArchive:
		var a log.SessionArchive
		if ev.Decode(&a) == nil {
			info.Archived = a.Archived
		}
	case log.KindSessionSchedule:
		var d log.SessionSchedule
		if ev.Decode(&d) == nil && !d.Removed && !f.First {
			info.FirstMessage = redact.Redact(d.Message, nil).Text
		}
	case log.KindUserMsg:
		var msg log.UserMsg
		if err := ev.Decode(&msg); err != nil {
			return err
		}
		if !f.First {
			info.FirstMessage = redact.Redact(msg.Text, nil).Text
			f.First = true
		}
		info.InFlight = true
	case log.KindEvalReq:
		info.InFlight = true
	case log.KindLLMResp:
		var resp log.LLMResp
		if err := ev.Decode(&resp); err != nil {
			return err
		}
		if len(resp.ToolCalls) == 0 {
			info.InFlight = false
		}
	case log.KindTurnEnd, log.KindSessionEnd, log.KindSessionResume:
		info.InFlight = false
	}
	return nil
}

// result is the listing once every event is in: a log with no session.start
// is no session's.
func (f *infoFold) result(id string) (SessionInfo, error) {
	if !f.Start {
		return f.Info, fmt.Errorf("core: session %s has no session.start", id)
	}
	return f.Info, nil
}

// Rename gives a session no process has open its name, or with "" takes
// the name back (ADR 39). The log's lock refuses a session another process
// has open; the one that has it records the name itself.
func (s Store) Rename(id, title, actor string) error {
	return s.record(id, log.KindSessionTitle, log.SessionTitle{Actor: actor, Title: title})
}

// Archive archives a session no process has open, or brings it back.
func (s Store) Archive(id string, archived bool, actor string) error {
	return s.record(id, log.KindSessionArchive, log.SessionArchive{Actor: actor, Archived: archived})
}

// record appends one record to a closed session's log, as recordSummary
// does: through the log's redaction boundary, holding its lock.
func (s Store) record(id string, kind log.Kind, payload any) error {
	path, err := s.path(id)
	if err != nil {
		return err
	}
	idx, truncated, err := s.index(id)
	if err != nil {
		return err
	}
	if truncated {
		return fmt.Errorf("core: resume the session to recover its incomplete log first")
	}
	writer, err := log.OpenAt(path, id, idx.Seq, idx.End)
	if err != nil {
		if writer != nil {
			writer.Close()
		}
		return err
	}
	defer writer.Close()
	_, err = writer.Append(kind, payload)
	return err
}

// Delete removes a session's log, which is all nodwyn keeps of a closed
// session: its scratch directory went when it closed (ADR 28). A session a
// process has open is refused by the lock that process holds.
func (s Store) Delete(id string) error {
	path, err := s.path(id)
	if err != nil {
		return err
	}
	if err := log.Remove(path); err != nil {
		return err
	}
	// The log's index and memory are the log's: they go with it.
	for _, derived := range []string{indexPath(s.Dir, id), memPath(s.Dir, id)} {
		if err := os.Remove(derived); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	infoCache.mu.Lock()
	delete(infoCache.entries, path)
	infoCache.mu.Unlock()
	return nil
}

// Info reads one session's metadata without opening it for append. A log
// whose size and modification time are unchanged since the last read is
// answered from memory: listing sessions should not reparse every log. One
// that grew is read from its index, which folds in only what it gained
// (log_index.go).
func (s Store) Info(id string) (SessionInfo, error) {
	info, _, err := s.infoAndSchedule(id)
	return info, err
}

// infoAndSchedule is Info with the session's schedule, which the same index
// holds: a list of every task asks for both, and decoding an index for each
// task, as a schedule read of its own would, took seconds for a few hundred.
func (s Store) infoAndSchedule(id string) (SessionInfo, ScheduleInfo, error) {
	path, err := s.path(id)
	if err != nil {
		return SessionInfo{ID: id}, ScheduleInfo{}, err
	}
	stat, err := os.Stat(path)
	if err != nil {
		return SessionInfo{ID: id}, ScheduleInfo{}, err
	}
	key := infoKey{stat.Size(), stat.ModTime()}
	infoCache.mu.Lock()
	cached, ok := infoCache.entries[path]
	infoCache.mu.Unlock()
	if ok && cached.key == key {
		return cached.info, cached.schedule.clone(), nil
	}
	idx, truncated, err := s.index(id)
	info := idx.Fold.Info
	info.Truncated = truncated
	if err != nil {
		return SessionInfo{ID: id}, ScheduleInfo{}, err
	}
	infoCache.mu.Lock()
	if infoCache.entries == nil {
		infoCache.entries = make(map[string]cachedInfo)
	}
	infoCache.entries[path] = cachedInfo{key, info, idx.Schedule.clone()}
	infoCache.mu.Unlock()
	return info, idx.Schedule, nil
}

type infoKey struct {
	size int64
	mod  time.Time
}
type cachedInfo struct {
	key      infoKey
	info     SessionInfo
	schedule ScheduleInfo
}

// infoCache is process-wide: every Store over the same directory reads the
// same files. Entries are keyed by path and invalidated by size or mtime.
var infoCache struct {
	mu      sync.Mutex
	entries map[string]cachedInfo
}

// List returns the sessions it can read, in sortable ID order, and an
// *UnreadableLogs naming the logs it cannot: an empty log a crash left, one
// still being created, one a newer nodwyn wrote, a copy under another name.
// One such log hides none of the others; the caller decides whether to say
// so. Any other error is the directory's. A missing directory is an empty
// store.
func (s Store) List() ([]SessionInfo, error) {
	entries, err := os.ReadDir(s.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var result []SessionInfo
	var bad UnreadableLogs
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, err := s.Info(strings.TrimSuffix(e.Name(), ".jsonl"))
		if err != nil {
			bad = append(bad, fmt.Errorf("%s: %w", e.Name(), err))
			continue
		}
		result = append(result, info)
	}
	if len(bad) > 0 {
		return result, bad
	}
	return result, nil
}

// UnreadableLogs is List's error for the logs in the sessions directory it
// could not read; the sessions it returns are all the others.
type UnreadableLogs []error

func (u UnreadableLogs) Error() string {
	msgs := make([]string, len(u))
	for i, err := range u {
		msgs[i] = err.Error()
	}
	return fmt.Sprintf("core: %d session log(s) cannot be read: %s", len(u), strings.Join(msgs, "; "))
}

// Readable is List's sessions when what it could not read was only some
// logs: their error is dropped, as a caller that lists tasks goes on
// without them. Any other error stays.
func Readable(infos []SessionInfo, err error) ([]SessionInfo, error) {
	var bad UnreadableLogs
	if errors.As(err, &bad) {
		return infos, nil
	}
	return infos, err
}
