package core

import (
	"fmt"
	"runtime"
	"slices"
	"strings"
	"sync"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/retrieval"
)

// Earlier tasks (ADR 50 §9): when the person lets it, for one project on
// this computer, index-search ranks the events of the project's other
// tasks and recall brings one back. Each log's words are its memory, kept
// beside it and brought up to date at the search (memory.go, ADR 51), so
// a search reads the postings of its own words from disk and holds
// nothing after: nodwyn's 78 local logs, 17,672 events, are about 10 MB
// of words.

// earlierOff says how searching earlier tasks is turned on, to the model,
// which can tell the person.
const earlierOff = "searching earlier tasks is off for this project: a person can turn it on for %s on this computer, on the console's Workspaces page or with :earlier on in the TUI"

// earlierAllowed reads, now, whether the person lets the model search this
// project's earlier tasks: workspaces.toml in the config directory, which
// only the console and the TUI on this computer write and no model or
// repository can, so turning it on or off counts from the next call.
func (s *Session) earlierAllowed() error {
	ws, err := config.LoadWorkspace(config.RegistryFile(s.opts.Paths), s.project())
	if err != nil {
		return err
	}
	if !ws.EarlierTasks {
		return fmt.Errorf(earlierOff, s.project())
	}
	return nil
}

// projectTasks are project's tasks, newest first, all but skip: a
// worktree's tasks are its repository's (ADR 34); a deleted task's log is
// gone, and the task with it.
func (s Store) projectTasks(project, skip string) ([]SessionInfo, error) {
	infos, err := Readable(s.List())
	if err != nil {
		return nil, err
	}
	slices.Reverse(infos) // IDs sort by start time
	var tasks []SessionInfo
	for _, info := range infos {
		if info.ID != skip && info.Project() == project {
			tasks = append(tasks, info)
		}
	}
	return tasks, nil
}

// memories reads the memory of tasks, in parallel, with the postings of
// the words keep allows. A log that does not read is left out.
func (s Store) memories(tasks []SessionInfo, keep func(string) bool) ([]SessionInfo, []*logMemory) {
	logs := make([]*logMemory, len(tasks))
	var wg sync.WaitGroup
	slots := make(chan struct{}, runtime.NumCPU())
	for i, info := range tasks {
		wg.Go(func() {
			slots <- struct{}{}
			defer func() { <-slots }()
			logs[i], _ = s.memory(info.ID, keep)
		})
	}
	wg.Wait()
	var keptTasks []SessionInfo
	var kept []*logMemory
	for i, l := range logs {
		if l != nil {
			keptTasks, kept = append(keptTasks, tasks[i]), append(kept, l)
		}
	}
	return keptTasks, kept
}

// taskLine names an earlier task in a result: its id, which recall takes,
// its name or first message, the day of its last event, and whether it is
// archived.
func taskLine(info SessionInfo) string {
	line := fmt.Sprintf("task %s, %q (%s", info.ID, cut(info.Title(), 80), info.LastEvent.Format("2006-01-02"))
	if info.Archived {
		line += ", archived"
	}
	return line + ")"
}

// searchTasks ranks the events of tasks as one collection, a word weighing
// by how many events of all of them hold it and a tie going to the newer
// task, and writes the best under their turns, each turn after its task.
// what names the tasks, with a %d for how many; how says how to bring one
// back.
func (s Store) searchTasks(tasks []SessionInfo, query string, limit int, what, how string) (string, error) {
	if len(retrieval.Words(query)) == 0 {
		return "", fmt.Errorf("the query has no words to look for")
	}
	tasks, logs := s.memories(tasks, queryWords(query))
	what = fmt.Sprintf(what, len(tasks))
	groups, events := rankTurns(logs, query, limit)
	if len(groups) == 0 {
		return fmt.Sprintf("No event of %s holds any of these words (%d events searched).", what, events), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "[the %d best of %d events in %s, best first, under the turns that hold them; %s]\n", found(groups), events, what, how)
	writeTurns(&b, logs, groups, func(i int) string { return taskLine(tasks[i]) + ", " }, func(i int, seq uint64) (string, error) {
		ev, err := s.eventAt(tasks[i].ID, seq)
		return eventText(ev), err
	})
	return b.String(), nil
}

// recallTask is recall of task, which must be one of project's.
func (s Store) recallTask(project, task string, seq uint64, start, count int64, budget int) (string, error) {
	info, err := s.Info(task)
	if err != nil {
		return "", err
	}
	if info.Project() != project {
		return "", fmt.Errorf("task %s is not one of this project's", task)
	}
	l, err := s.memory(task, noWords)
	if err != nil {
		return "", err
	}
	read := func(seq uint64) (log.Event, error) { return s.eventAt(task, seq) }
	return recallIn(l, read, seq, start, count, budget, taskLine(info)+": ")
}

// eventAt is the event at seq in session id's log, redacted for display.
func (s Store) eventAt(id string, seq uint64) (log.Event, error) {
	page, err := s.ReadLog(id, seq-1, seq, 1)
	if err != nil {
		return log.Event{}, err
	}
	if len(page.Events) != 1 || page.Events[0].Seq != seq {
		return log.Event{}, fmt.Errorf("task %s has no seq %d", id, seq)
	}
	return page.Events[0], nil
}

// searchEarlier is index-search's "earlier" scope: the events of the
// project's other tasks that best match query.
func (s *Session) searchEarlier(query string, limit int) (string, error) {
	if err := s.earlierAllowed(); err != nil {
		return "", err
	}
	store := Store{Dir: s.opts.Paths.SessionsDir}
	tasks, err := store.projectTasks(s.project(), s.id)
	if err != nil {
		return "", err
	}
	return store.searchTasks(tasks, query, limit, "this project's %d earlier tasks", "recall brings one back whole by its seq and task, and a turn's first message with what the turn did")
}

// recallEarlier is recall with a task: the event at seq in another task of
// this project, when the person lets the model search earlier tasks.
func (s *Session) recallEarlier(task string, seq uint64, start, count int64) (string, error) {
	if err := s.earlierAllowed(); err != nil {
		return "", err
	}
	return Store{Dir: s.opts.Paths.SessionsDir}.recallTask(s.project(), task, seq, start, count, s.rangeBytes())
}

// What a person at a terminal reads of one event at most.
const terminalBytes = 1 << 20

// SearchTasks is the search of earlier tasks for a person at a terminal
// (nodwyn memory search): every task of project, or only the one named.
// The person's leave decides what the model reads; a person reads their
// own logs.
func (s Store) SearchTasks(project, only, query string, limit int) (string, error) {
	tasks, err := s.projectTasks(project, "")
	if err != nil {
		return "", err
	}
	if only != "" {
		tasks = slices.DeleteFunc(tasks, func(info SessionInfo) bool { return info.ID != only })
	}
	return s.searchTasks(tasks, query, limit, "this repository's %d tasks", "nodwyn memory recall TASK SEQ brings one back whole, and a turn's first message with what the turn did")
}

// RecallTask is recall for a person at a terminal (nodwyn memory recall).
func (s Store) RecallTask(project, task string, seq uint64, start, count int64) (string, error) {
	return s.recallTask(project, task, seq, start, count, terminalBytes)
}
