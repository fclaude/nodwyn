package core

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/fclaude/nibb/internal/log"
	"github.com/fclaude/nibb/internal/redact"
)

// Store is a view of the session logs, not a second database.
type Store struct{ Dir string }

// SessionInfo summarizes durable history. InFlight means a turn had no
// recorded end; it does not claim that a process is currently alive.
type SessionInfo struct {
	ID           string
	Repo         string
	FirstMessage string
	LastEvent    time.Time
	InFlight     bool
	Truncated    bool
}

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
	for _, ev := range events {
		if ev.Session != id {
			return info, nil, fmt.Errorf("core: mixed session ids in %s", id)
		}
		info.LastEvent = ev.TS
		switch ev.Kind {
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

// Info reads one session's metadata without opening it for append.
func (s Store) Info(id string) (SessionInfo, error) { info, _, err := s.read(id); return info, err }

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
