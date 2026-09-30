package core

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

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
}

// Title is what a person reads a session by: the first sentence of the
// first message when it is short, else its first words.
func (i SessionInfo) Title() string {
	title, _, _ := strings.Cut(strings.TrimSpace(i.FirstMessage), "\n")
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

func (s Store) read(id string) (SessionInfo, []log.Event, error) {
	info := SessionInfo{ID: id}
	path, err := s.path(id)
	if err != nil {
		return info, nil, err
	}
	events, err := log.ReadFile(path)
	info.Truncated = errors.Is(err, log.ErrTruncated)
	if err != nil && !info.Truncated {
		return info, nil, err
	}
	var start log.SessionStart
	found := false
	for _, ev := range events {
		if ev.Kind != log.KindSessionStart {
			continue
		}
		if found {
			return info, nil, fmt.Errorf("core: duplicate session.start in %s", id)
		}
		if err := ev.Decode(&start); err != nil {
			return info, nil, err
		}
		found = true
	}
	if !found {
		return info, nil, fmt.Errorf("core: session %s has no session.start", id)
	}
	if !filepath.IsAbs(start.Repo) {
		return info, nil, fmt.Errorf("core: session %s has no absolute repository path", id)
	}
	info.Repo = start.Repo
	first := false
	written := map[string]bool{}
	for i, ev := range events {
		if ev.Session != id {
			return info, nil, fmt.Errorf("core: mixed session ids in %s", id)
		}
		if i == 0 {
			info.Started = ev.TS
		}
		info.LastEvent = ev.TS
		switch ev.Kind {
		case log.KindGuardDecision:
			var d log.GuardDecision
			if ev.Decode(&d) == nil && d.Path != "" && !written[d.Path] && len(info.Changes) < maxChanges &&
				(d.Decision == "allow" || (d.Decision == "confirm" && (d.Answer == "yes" || d.Answer == "turn"))) {
				written[d.Path] = true
				info.Changes = append(info.Changes, d.Path)
			}
		case log.KindRunState:
			var r log.RunState
			if ev.Decode(&r) == nil {
				info.Run = &r
			}
		case log.KindUserMsg:
			var msg log.UserMsg
			if err := ev.Decode(&msg); err != nil {
				return info, nil, err
			}
			if !first {
				info.FirstMessage = redact.Redact(msg.Text, nil).Text
				first = true
			}
			info.InFlight = true
		case log.KindEvalReq:
			info.InFlight = true
		case log.KindLLMResp:
			var resp log.LLMResp
			if err := ev.Decode(&resp); err != nil {
				return info, nil, err
			}
			if len(resp.ToolCalls) == 0 {
				info.InFlight = false
			}
		case log.KindTurnEnd, log.KindSessionEnd, log.KindSessionResume:
			info.InFlight = false
		}
	}
	return info, events, nil
}

// Info reads one session's metadata without opening it for append. A log
// whose size and modification time are unchanged since the last read is
// answered from memory: listing sessions should not reparse every log.
func (s Store) Info(id string) (SessionInfo, error) {
	path, err := s.path(id)
	if err != nil {
		return SessionInfo{ID: id}, err
	}
	stat, err := os.Stat(path)
	if err != nil {
		return SessionInfo{ID: id}, err
	}
	key := infoKey{stat.Size(), stat.ModTime()}
	infoCache.mu.Lock()
	cached, ok := infoCache.entries[path]
	infoCache.mu.Unlock()
	if ok && cached.key == key {
		return cached.info, nil
	}
	info, _, err := s.read(id)
	if err == nil {
		infoCache.mu.Lock()
		if infoCache.entries == nil {
			infoCache.entries = make(map[string]cachedInfo)
		}
		infoCache.entries[path] = cachedInfo{key, info}
		infoCache.mu.Unlock()
	}
	return info, err
}

type infoKey struct {
	size int64
	mod  time.Time
}
type cachedInfo struct {
	key  infoKey
	info SessionInfo
}

// infoCache is process-wide: every Store over the same directory reads the
// same files. Entries are keyed by path and invalidated by size or mtime.
var infoCache struct {
	mu      sync.Mutex
	entries map[string]cachedInfo
}

// List returns sessions in sortable ID order. Corruption is reported rather
// than quietly hiding a conversation. A missing directory is an empty store.
func (s Store) List() ([]SessionInfo, error) {
	entries, err := os.ReadDir(s.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var result []SessionInfo
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, err := s.Info(strings.TrimSuffix(e.Name(), ".jsonl"))
		if err != nil {
			return nil, fmt.Errorf("core: %s: %w", e.Name(), err)
		}
		result = append(result, info)
	}
	return result, nil
}
