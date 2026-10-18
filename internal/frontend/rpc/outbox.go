package rpc

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/schedule"
	"github.com/fclaude/nodwyn/internal/workflow"
)

// The outbox (ADR 53 §7). A workflow whose answer is to be posted does not
// post from the run: the run's model has no way to send anything out. When
// the run ends, the host reads its answer from the log and holds it as an
// outbox item; the person posts it (or edits and posts it, or discards it)
// from Needs you, and the host posts it with a key bound for exactly that
// call: the forge's review endpoint, for POST. "Without asking", a choice
// made on this computer, has the host post it as the run ends. A run that
// read a hostile pull request therefore has nothing to send with.

// The daemon's notice for each run's end, for a workflow that asks for one.
const (
	ranTitle = "nodwyn ran a workflow"
	ranBody  = "Open nodwyn to see what it did."
)

// pendingRun is a workflow run that has started and has not been read yet.
type pendingRun struct {
	workflow string
	item     string
	subject  string
	summary  string
	url      string
}

// runs are the workflow runs waiting for their task to go idle, by task.
// Its own lock, not the daemon's: a session's drain goroutine looks in it,
// and must never wait for the lifecycle lock a close holds while it drains.
type runs struct {
	mu      sync.Mutex
	pending map[string]pendingRun
}

func (r *runs) add(task string, run pendingRun) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pending == nil {
		r.pending = map[string]pendingRun{}
	}
	r.pending[task] = run
}

func (r *runs) has(task string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.pending[task]
	return ok
}

func (r *runs) take(task string) (pendingRun, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	run, ok := r.pending[task]
	delete(r.pending, task)
	return run, ok
}

// runEnded reads a workflow run's answer once its task is idle, and does
// what its workflow says with it. It runs on a goroutine of its own, since it
// takes the lifecycle lock.
func (s *server) runEnded(task string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	owned, open := s.sessions[task]
	if !open {
		s.runs.take(task)
		return
	}
	if owned.session.Busy() || owned.driver.Active() {
		return
	}
	events := owned.session.EventsSoFar()
	var started, ended uint64
	var outcome string
	for _, ev := range events {
		switch ev.Kind {
		case log.KindScheduleRun:
			var r log.ScheduleRun
			if ev.Decode(&r) == nil && r.Workflow != "" {
				started, ended, outcome = ev.Seq, 0, ""
			}
		case log.KindTurnEnd:
			var e log.TurnEnd
			if started > 0 && ended == 0 && ev.Decode(&e) == nil {
				ended, outcome = ev.Seq, e.Outcome
			}
		}
	}
	// The turn of the run has not ended yet: this idle was something else's.
	if started == 0 || ended == 0 {
		return
	}
	run, ok := s.runs.take(task)
	if !ok {
		return
	}
	state, err := s.flows().Read(run.workflow)
	if err != nil || state.Definition == nil || state.Definition.Removed {
		return
	}
	def := state.Definition.Definition
	now := time.Now()
	if def.Then.Notify == "run" {
		s.tell(ranTitle, ranBody)
	}
	if def.Then.Post != "review" {
		return
	}
	skip := func(why string) {
		_, _ = s.flows().Append(run.workflow, workflow.KindSkip, workflow.Skip{Item: run.item, Reason: why, At: now})
	}
	if outcome != "completed" {
		skip(fmt.Sprintf("the run for #%s did not finish (%s), so there is no answer to post", run.item, outcome))
		return
	}
	reply, found := owned.session.FinalReply()
	body := strings.TrimSpace(reply.Content)
	if !found || body == "" {
		skip(fmt.Sprintf("the run for #%s ended with no answer to post", run.item))
		return
	}
	if len(body) > workflow.MaxReview {
		body = strings.TrimSpace(body[:workflow.MaxReview-200]) + "\n\n[The rest of the answer is in the task: " + task + "]"
	}
	id := workflow.NewID()
	item := workflow.Outbox{ID: id, Kind: "review", Item: run.item, Subject: run.subject, Task: task, Summary: run.summary, URL: run.url, Body: body, At: now}
	if _, err := s.flows().Append(run.workflow, workflow.KindOutbox, item); err != nil {
		return
	}
	if def.Then.Auto {
		// Without asking: posted now, the same way, by the workflow's own
		// choice. If it cannot be (no POST binding, a locked keychain) it
		// stays in the outbox for the person.
		if err := s.deliver(context.Background(), run.workflow, def, item, body, "workflow"); err != nil {
			skip("could not post the review for #" + run.item + ": " + err.Error())
			s.watch.broadcast("outbox.changed")
			s.tell(answerTitle, answerBody)
		}
		s.watch.broadcast("outbox.changed")
		return
	}
	s.watch.broadcast("outbox.changed")
	s.tell(answerTitle, answerBody)
}

// deliver posts an outbox item as a review and records that it was, by whom.
// Under the lifecycle lock, released while the forge is asked.
func (s *server) deliver(ctx context.Context, id string, def workflow.Definition, item workflow.Outbox, body, by string) error {
	poller := s.poller()
	var err error
	s.slow("", func() { err = poller.PostReview(ctx, def.Source, item.Item, body) })
	if err != nil {
		return err
	}
	detail := ""
	if body != item.Body {
		detail = "edited before it was posted"
	}
	_, err = s.flows().Append(id, workflow.KindDelivered, workflow.Delivered{ID: item.ID, Fate: "posted", By: by, Detail: detail, At: time.Now()})
	return err
}

// outboxView is an outbox item as clients show it.
type outboxView struct {
	workflow.Outbox
	Workflow string `json:"workflow"`
	Name     string `json:"name"`
	When     string `json:"when"`
}

type outboxDecideParams struct {
	Workflow string `json:"workflow"`
	ID       string `json:"id"`
	Decision string `json:"decision"`
	Body     string `json:"body,omitempty"`
}

// outboxMethod answers outbox.list and outbox.decide. Posting a held answer is
// the person's act and a paired device may do it, as it may answer a
// question; changing what is posted, the words, is this computer's (or a
// passkey-signed person's).
func (s *server) outboxMethod(req request, client *peer) (any, *rpcError) {
	if req.Method == "outbox.list" {
		if err := params(req.Params, &struct{}{}); err != nil {
			return nil, err
		}
		states, err := s.flows().List()
		if err != nil {
			return nil, failure(-32000, err.Error())
		}
		now := time.Now()
		items := []outboxView{}
		for _, state := range states {
			for _, o := range state.Outbox {
				items = append(items, outboxView{o, state.ID, state.Definition.Name, schedule.Label(o.At.In(time.Local), now)})
			}
		}
		return items, nil
	}
	var p outboxDecideParams
	if err := params(req.Params, &p, "workflow", "id", "decision", "body"); err != nil {
		return nil, err
	}
	state, err := s.flows().Read(p.Workflow)
	if err != nil || state.Definition == nil || state.Definition.Removed {
		return nil, failure(-32000, fmt.Sprintf("no workflow %q", p.Workflow))
	}
	var item *workflow.Outbox
	for i := range state.Outbox {
		if state.Outbox[i].ID == p.ID {
			item = &state.Outbox[i]
		}
	}
	if item == nil {
		return nil, failure(-32000, "Nothing waits to be posted under that name: someone may have decided it")
	}
	switch p.Decision {
	case "discard":
		if _, err := s.flows().Append(p.Workflow, workflow.KindDelivered, workflow.Delivered{ID: item.ID, Fate: "discarded", By: client.actor, At: time.Now()}); err != nil {
			return nil, failure(-32000, err.Error())
		}
	case "post":
		body := item.Body
		if p.Body != "" && p.Body != item.Body {
			if why := client.refuseDevice("The words of an answer are changed on this computer, not from a paired device: post it as it is, or discard it"); why != nil {
				return nil, why
			}
			if len(p.Body) > workflow.MaxReview {
				return nil, failure(-32602, "A review is at most 60 KiB")
			}
			body = p.Body
		}
		if strings.TrimSpace(body) == "" {
			return nil, failure(-32602, "There is nothing to post")
		}
		if err := s.deliver(s.serving(), p.Workflow, state.Definition.Definition, *item, body, client.actor); err != nil {
			return nil, failure(-32000, "Could not post it: "+err.Error()+". It stays here.")
		}
	default:
		return nil, failure(-32602, `decision is "post" or "discard"`)
	}
	s.watch.broadcast("outbox.changed")
	return struct{}{}, nil
}
