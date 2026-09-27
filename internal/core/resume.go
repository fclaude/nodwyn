package core

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/fclaude/nibb/internal/event"
	"github.com/fclaude/nibb/internal/log"
)

// Resume opens an existing conversation in a fresh runtime. Configuration and
// init.scm are loaded normally; session-only approvals, prompts and Scheme
// definitions are not restored. It never starts a turn or repeats a tool call.
func Resume(o Options, id string) (_ *Session, err error) {
	if o.LogWriter != nil {
		return nil, errors.New("core: resume requires a file-backed session")
	}
	store := Store{Dir: o.Paths.SessionsDir}
	path, err := store.path(id)
	if err != nil {
		return nil, err
	}
	writer, err := log.Open(path)
	truncated := errors.Is(err, log.ErrTruncated)
	if err != nil && !truncated {
		return nil, err
	}
	var s *Session
	defer func() {
		if err != nil {
			writer.Close()
			if s != nil {
				removeScratch(s.scratch)
			}
		}
	}()
	info, history, err := store.read(id)
	if err != nil {
		return nil, err
	}
	if o.Repo != "" {
		requested, e := filepath.Abs(o.Repo)
		if e != nil {
			return nil, e
		}
		if filepath.Clean(requested) != filepath.Clean(info.Repo) {
			return nil, fmt.Errorf("core: session belongs to %s, not %s", info.Repo, requested)
		}
	}
	o.Repo = info.Repo
	st, err := os.Stat(o.Repo)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("core: repository is not a directory: %s", o.Repo)
	}
	o, err = resolveOptions(o)
	if err != nil {
		return nil, err
	}
	pending, err := unfinishedTools(history)
	if err != nil {
		return nil, err
	}
	s = &Session{opts: o, id: id, logw: writer, out: make(chan event.Event, o.EventBuffer+len(pending)+4), prompts: map[string]pendingConfirmation{}, secrets: map[string]pendingSecret{}}
	if err = s.restoreHistory(history); err != nil {
		return nil, err
	}
	if err = s.initialize(); err != nil {
		return nil, err
	}
	// Resolve missing results for the model's transcript without executing them.
	for _, call := range pending {
		if _, err = s.Record(log.KindToolResult, log.ToolResult{ID: call.ID, Name: call.Name, OK: false, Error: "Interrupted before a durable result was recorded. Outcome unknown; inspect the current state before deciding whether to retry."}); err != nil {
			return nil, err
		}
	}
	if truncated {
		if _, err = s.Record(log.KindNote, log.Note{Text: "Discarded an incomplete final log line while resuming."}); err != nil {
			return nil, err
		}
	}
	if _, err = s.Record(log.KindSessionResume, log.SessionStart{Context: s.loop.WindowContext(), Version: o.Version, Repo: o.Repo, Provider: o.Provider.Name(), Model: o.Config.Control.Model, URL: o.Config.Control.URL, Tools: len(s.loop.Tools())}); err != nil {
		return nil, err
	}
	if _, err = s.Record(log.KindNote, log.Note{Text: "Session resumed; session-only approvals and Scheme state were reset. No interrupted tool was rerun."}); err != nil {
		return nil, err
	}
	s.flushOutput()
	return s, nil
}

func unfinishedTools(events []log.Event) ([]log.ToolCall, error) {
	var pending []log.ToolCall
	for _, ev := range events {
		switch ev.Kind {
		case log.KindLLMResp:
			var resp log.LLMResp
			if err := ev.Decode(&resp); err != nil {
				return nil, err
			}
			pending = append(pending, resp.ToolCalls...)
		case log.KindToolResult:
			var result log.ToolResult
			if err := ev.Decode(&result); err != nil {
				return nil, err
			}
			// Ollama may reuse IDs in later responses. A result belongs to
			// the latest outstanding call, not an earlier interrupted turn.
			for i := len(pending) - 1; i >= 0; i-- {
				call := pending[i]
				if call.ID == result.ID && call.Name == result.Name {
					pending = append(pending[:i], pending[i+1:]...)
					break
				}
			}
		}
	}
	return pending, nil
}
