package core

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/fclaude/nodwyn/internal/config"
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
	// The log is read from its index (ADR 49 §10): the log's end is opened
	// for appending without reading what comes before it, and the session
	// holds only the events from where its earliest reader needs them, so
	// resuming a task that has run every day for months reads its last runs.
	idx, _, err := store.index(id)
	if err != nil {
		return nil, err
	}
	writer, err := log.OpenAt(path, id, idx.Seq, idx.End)
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
	// Another process may have appended between the index and the lock:
	// the index is brought to the end again, now that nothing else can.
	if writer.Seq() != idx.Seq {
		if idx, _, err = store.index(id); err != nil {
			return nil, err
		}
	}
	info := idx.Fold.Info
	history, spendBefore, err := store.suffix(id, idx)
	if err != nil {
		return nil, err
	}
	// The choices a task keeps are the latest records of their kinds,
	// wherever they are in the log: the index keeps those the session does
	// not hold, and they come first, as earlier events.
	choices := append(idx.Resume.choices(), history...)
	if o.Repo != "" {
		requested, e := filepath.Abs(o.Repo)
		if e != nil {
			return nil, e
		}
		if config.CanonicalPath(requested) != config.CanonicalPath(info.Repo) {
			return nil, fmt.Errorf("core: session belongs to %s, not %s", info.Repo, requested)
		}
	}
	if err := migrateNotes(o.Paths, info.Project()); err != nil {
		return nil, err
	}
	o.Repo = info.Repo
	// A conversation keeps the model it was switched to, and the effort it
	// asked for (ADR 40, ADR 41): the log says which. A choice config.toml no
	// longer offers does not make the task unopenable, and does not hand it to
	// another model either: the session is built on [control] only to be read
	// and to be chosen for, and asks no turn until a model is chosen (gone).
	gone := ""
	if c, label := lastModelChoice(choices); c != (ModelChoice{}) {
		if _, _, err := resolveChoice(o.Config, c); err != nil {
			gone = fmt.Sprintf("%s: %s", label, err)
		} else {
			o.Model = c
		}
	}
	// A worktree close removed, or a person deleted, is made again: the
	// conversation goes on where its files are (ADR 34).
	restored := false
	if info.Worktree != nil {
		var base string
		if restored, base, err = restoreWorktree(*info.Worktree); err != nil {
			return nil, err
		}
		// A new branch from where its base is now has that commit for its
		// base: session.resume and worktree.restore record it, and the
		// store reads it back from the latter.
		if base != "" {
			w := *info.Worktree
			w.BaseCommit = base
			info.Worktree = &w
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
	// A task keeps the judge it chose (ADR 47). One config.toml no longer
	// offers is not swapped for [judge]'s: the task has none until a judge
	// is chosen, so its plans and commands go nowhere nobody chose.
	judge, judgeGone := initialJudge(o), ""
	if rec, found := lastJudgeChoice(choices); found {
		judge = rec.Server
		if err := CheckJudge(o.Config, o.Keychain, rec.Server); err != nil {
			judge, judgeGone = "", fmt.Sprintf("%s: %s", rec.Server, err)
		}
	}
	delegates := initialDelegates(o)
	if rec, found := lastDelegates(choices); found {
		delegates = keepConfigured(o.Config, rec.Models)
	}
	s = &Session{opts: o, id: id, logw: writer, out: make(chan event.Event, o.EventBuffer+len(pending)+5), prompts: map[string]pendingConfirmation{}, secrets: map[string]pendingSecret{}, worktree: info.Worktree, modelLost: gone, judge: judge, delegates: delegates, spendBefore: spendBefore}
	// A task keeps its limits as it was left (ADR 49): a scheduled task is
	// resumed for every run, and one let run as long as it needs stays so.
	for _, ev := range choices {
		s.noteSchedule(ev)
	}
	if rec, found := lastLimits(choices); found {
		s.limitsOff, s.limitsActor = !rec.On, rec.Actor
	}
	if err = s.restoreHistory(history); err != nil {
		return nil, err
	}
	if err = s.initialize(); err != nil {
		return nil, err
	}
	if truncated {
		if _, err = s.Record(log.KindNote, log.Note{Text: "Discarded an incomplete final log line while resuming."}); err != nil {
			return nil, err
		}
	}
	if _, err = s.Record(log.KindSessionResume, log.SessionStart{Context: s.loop.WindowContext(), Version: o.Version, Repo: o.Repo, Provider: s.opts.Provider.Name(), Model: s.model.Model, URL: s.model.URL, Tools: len(s.loop.Tools()), Worktree: s.worktree}); err != nil {
		return nil, err
	}
	// Resolve missing results for the model's transcript without executing
	// them, after session.resume: recorded before it, a recovery result
	// would bridge the dead time between the crash and the resume into the
	// tool's time and the running and working time, which are nobody's.
	for _, call := range pending {
		if _, err = s.Record(log.KindToolResult, log.ToolResult{ID: call.ID, Name: call.Name, OK: false, Error: "Interrupted before a durable result was recorded. Outcome unknown; inspect the current state before deciding whether to retry."}); err != nil {
			return nil, err
		}
	}
	if restored {
		if _, err = s.Record(log.KindWorktreeRestore, log.WorktreeChange{Worktree: *s.worktree}); err != nil {
			return nil, err
		}
	}
	if _, err = s.Record(log.KindNote, log.Note{Text: "Session resumed; session-only approvals and Scheme state were reset. No interrupted tool was rerun."}); err != nil {
		return nil, err
	}
	if gone != "" {
		// The conversation says what it lost, rather than silently talking
		// to another model than the one it was left on.
		if _, err = s.Record(log.KindNote, log.Note{Text: "The model this conversation was left on is not available (" + gone + "), so no turn will be asked until a model is chosen for it."}); err != nil {
			return nil, err
		}
	}
	if judgeGone != "" {
		if _, err = s.Record(log.KindNote, log.Note{Text: "The judge this task chose is not available (" + judgeGone + "), so it has no judge until one is chosen."}); err != nil {
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
// the conversation does. The zero choice means no switch is recorded. label
// names it as the person knows it, by its model id and where it was, which is
// all the log keeps of a model config.toml no longer offers.
func lastModelChoice(events []log.Event) (c ModelChoice, label string) {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Kind != log.KindSessionModel {
			continue
		}
		var rec log.SessionModel
		if err := events[i].Decode(&rec); err != nil {
			return ModelChoice{}, ""
		}
		label = rec.Model
		if rec.Server != "" {
			label += " on " + rec.Server
		}
		if rec.Effort != "" {
			label += ", effort " + rec.Effort
		}
		// A named choice is restored by its name, and a model chosen on a
		// server by that pair: a choice is one or the other.
		if rec.Name != "" {
			return ModelChoice{Name: rec.Name, Effort: rec.Effort}, label
		}
		return ModelChoice{Server: rec.Server, Model: rec.Model, Effort: rec.Effort}, label
	}
	return ModelChoice{}, ""
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
