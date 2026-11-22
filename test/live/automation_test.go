//go:build live

package live

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fclaude/nodwyn/internal/log"
)

// The scenarios here are the work nobody watches (ADR 49, ADR 53): a task's
// schedule and a workflow's trigger, each once as a message for the model and
// once as a Scheme program. They go through the daemon as the console does,
// and wait for its own scheduler, which ticks every 30 seconds, rather than
// asking it to run now. The task's writes in its folder go ahead without
// asking, as every task's do; a command would wait for a person, so the
// messages ask for fs-write alone.

// openTask opens a task on the world's repository and returns its id.
func (w *world) openTask() string {
	w.t.Helper()
	var opened struct {
		ID string `json:"id"`
	}
	if err := w.call("session.open", map[string]any{"repo": w.repo}, &opened); err != nil || opened.ID == "" {
		w.t.Fatalf("session.open: %v", err)
	}
	w.ids = append(w.ids, opened.ID)
	return opened.ID
}

// scheduleEntry is a line of a schedule's history.
type scheduleEntry struct {
	Started time.Time `json:"started"`
	Manual  bool      `json:"manual"`
	Skipped string    `json:"skipped"`
	Outcome string    `json:"outcome"`
}

// waitScheduledRun waits until the task's schedule has a run that ended, and
// returns it.
func (w *world) waitScheduledRun(id string, limit time.Duration) scheduleEntry {
	w.t.Helper()
	for end := time.Now().Add(limit); ; time.Sleep(10 * time.Second) {
		var state struct {
			History   []scheduleEntry `json:"history"`
			Scheduler bool            `json:"scheduler"`
		}
		if err := w.call("schedule.read", map[string]any{"id": id}, &state); err != nil {
			w.t.Fatalf("schedule.read: %v", err)
		}
		if !state.Scheduler {
			w.t.Fatal("this daemon does not hold the scheduler")
		}
		for _, h := range state.History {
			if h.Skipped != "" {
				w.t.Fatalf("the scheduled run was skipped: %s", scrub(h.Skipped))
			}
			if h.Outcome != "" {
				return h
			}
		}
		if time.Now().After(end) {
			w.t.Fatalf("no scheduled run ended within %s", limit)
		}
	}
}

// fileSays waits for path, in the world's repository, to hold want.
func (w *world) fileSays(path, want string, limit time.Duration) {
	w.t.Helper()
	var got []byte
	for end := time.Now().Add(limit); ; time.Sleep(5 * time.Second) {
		got, _ = os.ReadFile(filepath.Join(w.repo, path))
		if strings.Contains(string(got), want) {
			return
		}
		if time.Now().After(end) {
			w.t.Fatalf("%s holds %q, not %q", path, got, want)
		}
	}
}

// evalResults are a session's program runs: their value, or their error.
func evalResults(evs []log.Event) []log.EvalResult {
	var out []log.EvalResult
	for _, ev := range evs {
		var r log.EvalResult
		if ev.Kind == log.KindEvalResult && ev.Decode(&r) == nil {
			out = append(out, r)
		}
	}
	return out
}

// A schedule's message, at a time a minute ahead, runs when the time comes,
// with nobody asking it to, and the model does what it says.
func TestAScheduledMessageRunsAtItsTime(t *testing.T) {
	w := newWorld(t, map[string]string{"README.md": "# Scheduled work\n"})
	w.record("schedule-message")
	word := "tide-" + number(t)
	id := w.openTask()
	at := time.Now().Add(time.Minute).Format("2006-01-02T15:04")
	var set json.RawMessage
	if err := w.call("schedule.set", map[string]any{"id": id, "rule": map[string]any{"once": at},
		"message": "Create the file scheduled.txt holding exactly the word " + word + ", using fs-write. Do nothing else: no command, no other file."}, &set); err != nil {
		t.Fatalf("schedule.set: %v", err)
	}
	run := w.waitScheduledRun(id, 20*time.Minute)
	if run.Manual {
		t.Error("the run was started by hand, not by the scheduler")
	}
	if run.Outcome != "completed" {
		t.Errorf("the scheduled run ended %s", run.Outcome)
	}
	w.fileSays("scheduled.txt", word, time.Minute)
	if !strings.Contains(w.logText(id), `"kind":"schedule.run"`) {
		t.Error("the log has no schedule.run record")
	}
}

// A schedule's program, every five minutes, runs in the task's harness when
// it is due: its own write, then a turn of the model it asks for.
func TestAScheduledProgramRunsEveryFiveMinutes(t *testing.T) {
	w := newWorld(t, map[string]string{"README.md": "# Scheduled code\n"})
	w.record("schedule-program")
	word := "ember-" + number(t)
	id := w.openTask()
	program := `(fs-write "from-program.txt" (string-append "` + word + `" " " (number->string (* 6 7))))
(add-message "Reply with the single word ready, and nothing else.")
(assoc-ref (model-turn "control" '((tools . #f))) 'content)`
	var set json.RawMessage
	if err := w.call("schedule.set", map[string]any{"id": id, "rule": map[string]any{"every": 5}, "program": program}, &set); err != nil {
		t.Fatalf("schedule.set: %v", err)
	}
	run := w.waitScheduledRun(id, 15*time.Minute)
	// One run is enough: the schedule is removed before the next.
	if err := w.call("schedule.set", map[string]any{"id": id, "remove": true}, &set); err != nil {
		t.Errorf("removing the schedule: %v", err)
	}
	if run.Outcome != "completed" {
		t.Errorf("the scheduled program ended %s", run.Outcome)
	}
	w.fileSays("from-program.txt", word+" 42", time.Minute)
	results := evalResults(w.events(id))
	if len(results) == 0 || results[len(results)-1].Error != "" {
		t.Fatalf("the program's result: %+v", results)
	}
	if got := results[len(results)-1].Value; !strings.Contains(strings.ToLower(got), "ready") {
		t.Errorf("the model's turn in the program answered %q", scrub(got))
	}
}

// workflowFire is one event a workflow acted on.
type workflowFire struct {
	Item string `json:"item"`
	Kind string `json:"kind"`
	Task string `json:"task"`
}

// commandWorkflow makes a workflow whose source is a command that prints
// the lines of items.jsonl, a file outside the repository the scenario
// writes to, polled every minute.
func (w *world) commandWorkflow(name string, run map[string]any) (id, items string) {
	w.t.Helper()
	items = filepath.Join(w.home, "items.jsonl")
	if err := os.WriteFile(items, []byte(`{"id":"T-1","version":"1","title":"An old ticket, already handled"}`+"\n"), 0o600); err != nil {
		w.t.Fatal(err)
	}
	definition := map[string]any{"name": name, "every": 1, "routing": "one", "repo": w.repo, "then": map[string]any{},
		"source": map[string]any{"kind": "command", "command": []string{"cat", items}}}
	for k, v := range run {
		definition[k] = v
	}
	var view struct {
		ID     string `json:"id"`
		Seeded bool   `json:"seeded"`
	}
	if err := w.call("workflow.write", map[string]any{"definition": definition}, &view); err != nil || view.ID == "" {
		w.t.Fatalf("workflow.write: %v", err)
	}
	// The first check records what the source has, and acts on none of it.
	for end := time.Now().Add(2 * time.Minute); !view.Seeded; time.Sleep(5 * time.Second) {
		if err := w.call("workflow.read", map[string]any{"id": view.ID}, &view); err != nil {
			w.t.Fatalf("workflow.read: %v", err)
		}
		if time.Now().After(end) {
			w.t.Fatal("the workflow never checked its source")
		}
	}
	return view.ID, items
}

// addItem makes the command source print one more item.
func addItem(t *testing.T, items, line string) {
	f, err := os.OpenFile(items, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(line + "\n"); err != nil {
		t.Fatal(err)
	}
}

// waitFire waits for the workflow to act on item, and for that run's turn to
// end, and returns the fire.
func (w *world) waitFire(id, item string, limit time.Duration) workflowFire {
	w.t.Helper()
	for end := time.Now().Add(limit); ; time.Sleep(10 * time.Second) {
		var view struct {
			Fires    []workflowFire `json:"fires"`
			LastSkip *struct {
				Item, Reason string
			} `json:"last_skip"`
		}
		if err := w.call("workflow.read", map[string]any{"id": id}, &view); err != nil {
			w.t.Fatalf("workflow.read: %v", err)
		}
		for _, f := range view.Fires {
			if f.Item == "T-1" {
				w.t.Fatal("the workflow acted on the item its source had before it began")
			}
			if f.Item == item && f.Task != "" && len(outcomes(w.events(f.Task))) > 0 {
				if !contains(w.ids, f.Task) {
					w.ids = append(w.ids, f.Task)
				}
				return f
			}
		}
		if time.Now().After(end) {
			skip := ""
			if view.LastSkip != nil {
				skip = scrub(view.LastSkip.Item + ": " + view.LastSkip.Reason)
			}
			w.t.Fatalf("the workflow did not act on %s within %s (fires %+v; last skip %s)", item, limit, view.Fires, skip)
		}
	}
}

// A workflow's trigger, a new item its command source prints, runs its
// message in a task, with the event attached, and the model acts on it; the
// item the source had before the workflow began is not acted on.
func TestAWorkflowMessageRunsOnANewItem(t *testing.T) {
	w := newWorld(t, map[string]string{"README.md": "# Tickets\n"})
	w.record("workflow-message")
	word := "harbour-" + number(t)
	id, items := w.commandWorkflow("Tickets to notes", map[string]any{
		"message": "A new ticket is attached to this message. Write its id, then a space, then the word its body gives, into ticket.txt using fs-write. Do nothing else: no command, no other file.",
	})
	addItem(t, items, `{"id":"T-2","version":"1","title":"Record a word","body":"The word is `+word+`."}`)
	fire := w.waitFire(id, "T-2", 15*time.Minute)
	if fire.Kind != "opened" {
		t.Errorf("the event was %s, not opened", fire.Kind)
	}
	w.fileSays("ticket.txt", "T-2 "+word, time.Minute)
	if o := outcomes(w.events(fire.Task)); o[len(o)-1] != "completed" {
		t.Errorf("the run's turn ended %v", o)
	}
}

// A workflow's program runs on a new item in the task's harness: its own
// write, and a turn it asks of the model, which reads the attached event.
func TestAWorkflowProgramRunsOnANewItem(t *testing.T) {
	w := newWorld(t, map[string]string{"README.md": "# Alerts\n"})
	w.record("workflow-program")
	word := "lantern-" + number(t)
	program := `(fs-write "alert-seen.txt" "program ran")
(add-message "An alert is attached above. Reply with the word its body gives, and nothing else.")
(let ((reply (assoc-ref (model-turn "control" '((tools . #f))) 'content)))
  (fs-write "alert-word.txt" reply)
  reply)`
	id, items := w.commandWorkflow("Alerts to a file", map[string]any{"program": program})
	addItem(t, items, `{"id":"A-7","version":"1","title":"Disk alert","body":"The word is `+word+`."}`)
	fire := w.waitFire(id, "A-7", 15*time.Minute)
	w.fileSays("alert-seen.txt", "program ran", time.Minute)
	w.fileSays("alert-word.txt", word, time.Minute)
	results := evalResults(w.events(fire.Task))
	if len(results) == 0 || results[len(results)-1].Error != "" {
		t.Fatalf("the program's result: %+v", results)
	}
}
