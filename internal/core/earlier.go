package core

import (
	"fmt"
	"os"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/redact"
	"github.com/fclaude/nodwyn/internal/retrieval"
)

// Earlier tasks (ADR 50 §9): when the person lets it, for one project on
// this computer, index-search ranks the events of the project's other
// tasks and recall brings one back. Each log's words are indexed in memory
// once per process, shared by its sessions as Store.Info's cache is, and
// again only when the log changed. A hit's text is read from its log again
// by its seq, so what is kept is words and headings: nodwyn's 73 earlier
// tasks in the 78 local logs took 3.6 seconds, in parallel, and 87 MB.
var earlierCache = struct {
	mu      sync.Mutex
	entries map[string]*earlierLog
}{entries: map[string]*earlierLog{}}

// earlierLog is one log's conversation as earlier tasks' searches read it,
// and the size and time of the log it was read from. Built once, it is
// only read, by any session.
type earlierLog struct {
	size  int64
	mtime time.Time
	conv  *conversation
}

// earlierOff says how searching earlier tasks is turned on, to the model,
// which can tell the person.
const earlierOff = "searching earlier tasks is off for this project: a person can turn it on for %s in the console, Connections › Workspaces, on this computer"

// earlierAllowed reads, now, whether the person lets the model search this
// project's earlier tasks: workspaces.toml in the config directory, which
// only the console on this computer writes and no model or repository can,
// so turning it on or off counts from the next call.
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

// earlierTasks are the project's other tasks, newest first, each with its
// indexed conversation: a worktree's tasks are its repository's (ADR 34);
// a deleted task's log is gone, and the task with it. A log that does not
// read is left out.
func (s *Session) earlierTasks() ([]SessionInfo, []*conversation, error) {
	store := Store{Dir: s.opts.Paths.SessionsDir}
	infos, err := store.List()
	if err != nil {
		return nil, nil, err
	}
	slices.Reverse(infos) // IDs sort by start time
	var tasks []SessionInfo
	for _, info := range infos {
		if info.ID != s.id && info.Project() == s.project() {
			tasks = append(tasks, info)
		}
	}
	convs := make([]*conversation, len(tasks))
	var wg sync.WaitGroup
	slots := make(chan struct{}, runtime.NumCPU())
	for i, info := range tasks {
		wg.Go(func() {
			slots <- struct{}{}
			defer func() { <-slots }()
			convs[i], _ = earlierIndex(store, info.ID)
		})
	}
	wg.Wait()
	var keptTasks []SessionInfo
	var kept []*conversation
	for i, c := range convs {
		if c != nil {
			keptTasks, kept = append(keptTasks, tasks[i]), append(kept, c)
		}
	}
	return keptTasks, kept, nil
}

// earlierIndex is the indexed conversation of the task id, from the cache
// when its log has not changed since.
func earlierIndex(store Store, id string) (*conversation, error) {
	path, err := store.path(id)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	earlierCache.mu.Lock()
	e := earlierCache.entries[path]
	earlierCache.mu.Unlock()
	if e != nil && e.size == st.Size() && e.mtime.Equal(st.ModTime()) {
		return e.conv, nil
	}
	_, events, err := store.read(id)
	if err != nil {
		return nil, err
	}
	c := &conversation{built: true, bySeq: map[uint64]int{}, calls: map[string]log.ToolCall{}}
	for _, ev := range events {
		if len(ev.Payload) > 0 {
			if ev.Payload, _, err = redact.JSON(ev.Payload, nil); err != nil {
				return nil, err
			}
		}
		c.through = ev.Seq
		c.add(ev)
	}
	// Words and headings stay; texts and arguments are read again.
	c.calls = nil
	for i := range c.found {
		c.found[i].text = ""
		for j := range c.found[i].calls {
			c.found[i].calls[j].Args = nil
		}
	}
	earlierCache.mu.Lock()
	earlierCache.entries[path] = &earlierLog{size: st.Size(), mtime: st.ModTime(), conv: c}
	earlierCache.mu.Unlock()
	return c, nil
}

// earlierText is the text of the event at seq in task id's log, read again
// from the log by its seq, as eventText made it when it was indexed.
func earlierText(store Store, id string, seq uint64) (string, error) {
	page, err := store.ReadLog(id, seq-1, 0, 1)
	if err != nil {
		return "", err
	}
	if len(page.Events) != 1 || page.Events[0].Seq != seq {
		return "", fmt.Errorf("task %s has no seq %d", id, seq)
	}
	return eventText(page.Events[0]), nil
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

// searchEarlier is index-search's "earlier" scope: the events of the
// project's other tasks that best match query, ranked as one collection.
func (s *Session) searchEarlier(query string, limit int) (string, error) {
	if err := s.earlierAllowed(); err != nil {
		return "", err
	}
	tasks, convs, err := s.earlierTasks()
	if err != nil {
		return "", err
	}
	indexes := make([]*retrieval.Index, len(convs))
	events := 0
	for i, c := range convs {
		indexes[i] = &c.index
		events += c.index.Len()
	}
	found := retrieval.SearchAll(indexes, query, limit, nil)
	if len(found) == 0 {
		return fmt.Sprintf("No event of this project's %d earlier tasks holds any of these words (%d events searched).", len(tasks), events), nil
	}
	store := Store{Dir: s.opts.Paths.SessionsDir}
	var b strings.Builder
	fmt.Fprintf(&b, "[the %d best of %d events in this project's %d earlier tasks, best first; recall brings one back whole by its seq and task]\n", len(found), events, len(tasks))
	for _, f := range found {
		e := convs[f.Index].found[f.Doc]
		fmt.Fprintf(&b, "%s: %s\n", taskLine(tasks[f.Index]), e.heading())
		text, err := earlierText(store, tasks[f.Index].ID, e.seq)
		if err != nil {
			continue // the log changed under the search; the heading says what it was
		}
		for _, l := range bestLines(text, f.Matched) {
			fmt.Fprintf(&b, "  %d: %s\n", l.n, cut(l.text, searchLineCut))
		}
	}
	return b.String(), nil
}

// recallEarlier is recall with a task: the event at seq in another task of
// this project, when the person lets the model search earlier tasks.
func (s *Session) recallEarlier(task string, seq uint64, start, count int64) (string, error) {
	if err := s.earlierAllowed(); err != nil {
		return "", err
	}
	store := Store{Dir: s.opts.Paths.SessionsDir}
	info, err := store.Info(task)
	if err != nil {
		return "", err
	}
	if info.Project() != s.project() {
		return "", fmt.Errorf("task %s is not one of this project's", task)
	}
	c, err := earlierIndex(store, task)
	if err != nil {
		return "", err
	}
	doc, err := c.find(seq)
	if err != nil {
		return "", err
	}
	text, err := earlierText(store, task, seq)
	if err != nil {
		return "", err
	}
	return c.show(doc, text, taskLine(info)+": ", start, count, s.rangeBytes())
}
