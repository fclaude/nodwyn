package core

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/fclaude/nodwyn/internal/event"
	"github.com/fclaude/nodwyn/internal/log"
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
				s.closeMCP()
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
	// A conversation keeps the model it was switched to (ADR 40): the log
	// says which, and a choice config.toml no longer offers falls back to
	// the [control] model rather than making the task unopenable.
	gone := ""
	if c := lastModelChoice(history); c != (ModelChoice{}) {
		if _, err := resolveModel(o.Config, c); err != nil {
			gone = err.Error()
		} else {
			o.Model = c
		}
	}
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
	if _, err = s.Record(log.KindSessionResume, log.SessionStart{Context: s.loop.WindowContext(), Version: o.Version, Repo: o.Repo, Provider: s.opts.Provider.Name(), Model: s.model.Model, URL: s.model.URL, Tools: len(s.loop.Tools())}); err != nil {
		return nil, err
	}
	if _, err = s.Record(log.KindNote, log.Note{Text: "Session resumed; session-only approvals and Scheme state were reset. No interrupted tool was rerun."}); err != nil {
		return nil, err
	}
	if gone != "" {
		// The conversation says what it lost, rather than silently talking
		// to another model than the one it was left on.
		if _, err = s.Record(log.KindNote, log.Note{Text: "The model this conversation was switched to is no longer configured, so it talks to the [control] model again: " + gone}); err != nil {
			return nil, err
		}
	}
	// A task taken up again is no longer put away (ADR 39).
	if info.Archived {
		if _, err = s.Record(log.KindSessionArchive, log.SessionArchive{Archived: false}); err != nil {
			return nil, err
		}
	}
	s.markStarted()
	s.flushOutput()
	return s, nil
}

// lastModelChoice is the model the log last switched this conversation to
// (ADR 40), if it switched one: the person's choice outlives the process, as
// the conversation does. The zero choice means no switch is recorded.
func lastModelChoice(events []log.Event) ModelChoice {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Kind != log.KindSessionModel {
			continue
		}
		var rec log.SessionModel
		if err := events[i].Decode(&rec); err != nil {
			return ModelChoice{}
		}
		// A named choice is restored by its name, and a model chosen on a
		// server by that pair: a choice is one or the other.
		if rec.Name != "" {
			return ModelChoice{Name: rec.Name}
		}
		return ModelChoice{Server: rec.Server, Model: rec.Model}
	}
	return ModelChoice{}
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
