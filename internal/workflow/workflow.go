// Package workflow keeps the log of a workflow whose events come from a
// source (ADR 53 §8, amended for slice 3): its definition, what its source
// had when last seen, the task of each subject, and what each event came
// to. A timetable whose runs go to one task needs none of this and keeps its
// schedule in its task's log (ADR 49).
//
// The logs are in nodwyn's data directory (workflows/<id>.jsonl), written by
// the daemon that holds scheduler.lock when a person on this computer makes
// or changes a workflow, and as its source is polled; nothing in a
// repository writes one, and no primitive does. Every record is redacted as
// it is written, as a task's records are.
package workflow

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/fclaude/nodwyn/internal/redact"
	"github.com/fclaude/nodwyn/internal/watch"
)

// Definition is what a person chose: the source and how often it is
// polled, the filters an event must pass, the message each run sends, where
// runs go (a task per subject, or one task for every event), and the
// choices of the tasks it opens (folder, worktree, model, spend per run and
// per day); like any task, those have no turn limits unless a person adds
// some to one (ADR 49, amended 2026-10-27). Concurrency is how many of its runs may go at
// once (0: two); Existing handles what the source has when the workflow
// starts, which is otherwise only recorded.
type Definition struct {
	Name        string         `json:"name"`
	Source      watch.Source   `json:"source"`
	Every       int            `json:"every"`
	Filters     []watch.Filter `json:"filters,omitempty"`
	Message     string         `json:"message"`
	Routing     string         `json:"routing"`
	Repo        string         `json:"repo"`
	Worktree    bool           `json:"worktree,omitempty"`
	Model       string         `json:"model,omitempty"`
	Cost        float64        `json:"cost"`
	DayCost     float64        `json:"day_cost,omitempty"`
	Concurrency int            `json:"concurrency,omitempty"`
	Existing    bool           `json:"existing,omitempty"`
	Then        Then           `json:"then"`
	Paused      bool           `json:"paused,omitempty"`
}

// Then is what happens with a run's answer (ADR 53 §7). Post is "" (it stays
// in the task), "review" (a review comment on the pull request, never an
// approval or a request for changes: those are the person's) or "draft" (a
// draft reply in the email's thread, never sent: sending is the person's).
// Held answers
// wait in the outbox for the person's yes, unless Auto, a choice made on this
// computer. Notify is "" (when the run needs the person, which Needs you and
// the daemon's notices already say), "run" (a desktop notice at the end of
// each run) or "never".
type Then struct {
	Post   string `json:"post,omitempty"`
	Auto   bool   `json:"auto,omitempty"`
	Notify string `json:"notify,omitempty"`
}

// MaxReview bounds a posted answer: a forge refuses much more.
const MaxReview = 60 << 10

// Where runs go.
const (
	PerSubject = "subject"
	OneTask    = "one"
)

// MaxEvents is how many events one poll may fire; more pauses the workflow
// and asks (ADR 53 §10).
const MaxEvents = 20

func (d Definition) Validate() error {
	switch {
	case strings.TrimSpace(d.Name) == "" || len(d.Name) > 200:
		return fmt.Errorf("a workflow needs a name of at most 200 characters")
	case strings.TrimSpace(d.Message) == "" || len(d.Message) > 1<<20:
		return fmt.Errorf("a workflow needs a message of at most 1 MiB")
	case d.Every < 1 || d.Every > 24*60:
		return fmt.Errorf("a source is checked every 1 minute to 24 hours")
	case d.Routing != PerSubject && d.Routing != OneTask:
		return fmt.Errorf("runs go to a task per subject or one task, not %q", d.Routing)
	case !filepath.IsAbs(d.Repo):
		return fmt.Errorf("a workflow's tasks work in a folder, given as an absolute path")
	case d.Concurrency < 0 || d.Concurrency > 10:
		return fmt.Errorf("at most 10 runs of a workflow go at once")
	}
	for _, cost := range []float64{d.Cost, d.DayCost} {
		if math.IsNaN(cost) || math.IsInf(cost, 0) || cost < 0 || cost > 1e9 {
			return fmt.Errorf("a spend limit is between 0 and 1 billion dollars")
		}
	}
	if err := d.Source.Validate(); err != nil {
		return err
	}
	switch d.Then.Post {
	case "":
		if d.Then.Auto {
			return fmt.Errorf("\"without asking\" is about posting an answer, and none is posted")
		}
	case "review":
		if d.Source.Kind != watch.GiteaPulls && d.Source.Kind != watch.GitHubPulls {
			return fmt.Errorf("a review is posted to a pull request, which this source has none of")
		}
	case "draft":
		if d.Source.Kind != watch.Gmail {
			return fmt.Errorf("a draft reply is saved in a mailbox, which this source has none of")
		}
	default:
		return fmt.Errorf("the answer is posted as a review, saved as a draft reply, or left in the task, not %q", d.Then.Post)
	}
	switch d.Then.Notify {
	case "", "run", "never":
	default:
		return fmt.Errorf("a notice is sent for each run, only when the run needs you, or never, not %q", d.Then.Notify)
	}
	for _, f := range d.Filters {
		if err := f.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// Runs is how many runs may go at once.
func (d Definition) Runs() int {
	if d.Concurrency == 0 {
		return 2
	}
	return d.Concurrency
}

// The kinds of record.
const (
	KindDefine = "workflow.define"
	KindSeen   = "workflow.seen"
	KindFire   = "workflow.fire"
	KindSkip   = "workflow.skip"
	// An answer waiting for the person to post it, and what came of it.
	KindOutbox    = "workflow.outbox"
	KindDelivered = "workflow.delivered"
)

// Define records a definition, who chose it, and whether it was removed.
type Define struct {
	Definition
	Actor   string `json:"actor"`
	Removed bool   `json:"removed,omitempty"`
}

// Seen is what the source had: each item's id and version.
type Seen struct {
	Items map[string]string `json:"items"`
}

// Fire is an event that started a run: its item, what happened, the subject
// and task it went to, and the summary the run carries.
type Fire struct {
	Item    string    `json:"item"`
	Version string    `json:"version"`
	Kind    string    `json:"kind"`
	Subject string    `json:"subject"`
	Task    string    `json:"task"`
	Summary string    `json:"summary"`
	At      time.Time `json:"at"`
}

// Outbox is a run's answer held to be posted: what it would be (a review),
// where (the item of the source), the task that wrote it and the text. ID
// names it among the workflow's.
type Outbox struct {
	ID      string    `json:"id"`
	Kind    string    `json:"kind"`
	Item    string    `json:"item"`
	Subject string    `json:"subject,omitempty"`
	Task    string    `json:"task"`
	Summary string    `json:"summary"`
	URL     string    `json:"url,omitempty"`
	Body    string    `json:"body"`
	At      time.Time `json:"at"`
	// Reply is, for a draft, whom it answers and in which thread.
	Reply *watch.Reply `json:"reply,omitempty"`
}

// Delivered says what came of an outbox item: posted, or discarded, by whom.
type Delivered struct {
	ID     string    `json:"id"`
	Fate   string    `json:"fate"`
	By     string    `json:"by"`
	Detail string    `json:"detail,omitempty"`
	At     time.Time `json:"at"`
}

// Skip is a poll or an event that did not come to a run, and why.
type Skip struct {
	Item   string    `json:"item,omitempty"`
	Reason string    `json:"reason"`
	At     time.Time `json:"at"`
}

type record struct {
	Seq     uint64          `json:"seq"`
	TS      time.Time       `json:"ts"`
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload"`
}

// State is a workflow's log folded: Seeded says its source was seen once,
// Subjects are the task of each subject (the one task's under "").
type State struct {
	ID         string            `json:"id"`
	Definition *Define           `json:"definition,omitempty"`
	Sequence   uint64            `json:"sequence"`
	Seen       map[string]string `json:"seen,omitempty"`
	Seeded     bool              `json:"seeded"`
	Subjects   map[string]string `json:"subjects,omitempty"`
	Fires      []Fire            `json:"fires,omitempty"`
	LastSkip   *Skip             `json:"last_skip,omitempty"`
	// Outbox are the answers still waiting to be posted, oldest first, and
	// Delivered the latest few fates.
	Outbox    []Outbox    `json:"outbox"`
	Delivered []Delivered `json:"delivered,omitempty"`
	seq       uint64
}

// recent is how many fires State keeps.
const recent = 20

func (s *State) observe(r record) {
	s.seq = r.Seq
	switch r.Kind {
	case KindDefine:
		var d Define
		if json.Unmarshal(r.Payload, &d) == nil {
			s.Definition, s.Sequence = &d, r.Seq
		}
	case KindSeen:
		var p Seen
		if json.Unmarshal(r.Payload, &p) == nil {
			s.Seen, s.Seeded = p.Items, true
		}
	case KindFire:
		var p Fire
		if json.Unmarshal(r.Payload, &p) == nil {
			if s.Subjects == nil {
				s.Subjects = map[string]string{}
			}
			s.Subjects[p.Subject] = p.Task
			s.Fires = append(s.Fires, p)
			if len(s.Fires) > recent {
				s.Fires = slices.Clone(s.Fires[len(s.Fires)-recent:])
			}
		}
	case KindSkip:
		var p Skip
		if json.Unmarshal(r.Payload, &p) == nil {
			s.LastSkip = &p
		}
	case KindOutbox:
		var p Outbox
		if json.Unmarshal(r.Payload, &p) == nil {
			s.Outbox = append(s.Outbox, p)
		}
	case KindDelivered:
		var p Delivered
		if json.Unmarshal(r.Payload, &p) == nil {
			s.Outbox = slices.DeleteFunc(s.Outbox, func(o Outbox) bool { return o.ID == p.ID })
			s.Delivered = append(s.Delivered, p)
			if len(s.Delivered) > recent {
				s.Delivered = slices.Clone(s.Delivered[len(s.Delivered)-recent:])
			}
		}
	}
}

// Store is the workflows' directory.
type Store struct{ Dir string }

func (s Store) path(id string) (string, error) {
	if id == "" || strings.ContainsAny(id, `/\.`) {
		return "", fmt.Errorf("no workflow %q", id)
	}
	return filepath.Join(s.Dir, id+".jsonl"), nil
}

// NewID names a new workflow.
func NewID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return time.Now().UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(b)
}

// Read folds a workflow's log.
func (s Store) Read(id string) (State, error) {
	path, err := s.path(id)
	if err != nil {
		return State{}, err
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return State{}, fmt.Errorf("no workflow %q", id)
	}
	if err != nil {
		return State{}, err
	}
	defer f.Close()
	state := State{ID: id, Outbox: []Outbox{}}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64<<10), 16<<20)
	for scanner.Scan() {
		var r record
		// A torn last line, from a crash mid-write, is left out.
		if json.Unmarshal(scanner.Bytes(), &r) == nil {
			state.observe(r)
		}
	}
	return state, scanner.Err()
}

// List folds every workflow's log, removed ones left out.
func (s Store) List() ([]State, error) {
	entries, err := os.ReadDir(s.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []State
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".jsonl")
		if !ok {
			continue
		}
		state, err := s.Read(id)
		if err != nil || state.Definition == nil || state.Definition.Removed {
			continue
		}
		out = append(out, state)
	}
	return out, nil
}

// Append redacts and records one record after the log's last. Only the
// daemon holding scheduler.lock writes, under its own lock, so records are
// not interleaved.
func (s Store) Append(id, kind string, payload any) (State, error) {
	state, err := s.Read(id)
	if err != nil && !strings.HasPrefix(err.Error(), "no workflow") {
		return State{}, err
	}
	state.ID = id
	raw, err := json.Marshal(payload)
	if err != nil {
		return State{}, err
	}
	if raw, _, err = redact.JSON(raw, nil); err != nil {
		return State{}, err
	}
	r := record{Seq: state.seq + 1, TS: time.Now().UTC(), Kind: kind, Payload: raw}
	line, err := json.Marshal(r)
	if err != nil {
		return State{}, err
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return State{}, err
	}
	path, err := s.path(id)
	if err != nil {
		return State{}, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return State{}, err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		f.Close()
		return State{}, err
	}
	if err := f.Close(); err != nil {
		return State{}, err
	}
	state.observe(r)
	return state, nil
}

// Sentence says the workflow in words, as every client shows it: "When a
// pull request on fc/nodwyn (git.example.com) is opened or gets new commits,
// if author is not "me", checked every 2 minutes; each in a task of its own".
func (d Definition) Sentence() string {
	var b strings.Builder
	on := d.Source.On
	if len(on) == 0 {
		on = []string{"opened", "updated"}
	}
	opened, updated := slices.Contains(on, "opened"), slices.Contains(on, "updated")
	switch d.Source.Kind {
	case watch.GiteaPulls, watch.GitHubPulls:
		host := "GitHub"
		if d.Source.Server != "" {
			if u, err := url.Parse(d.Source.Server); err == nil && u.Host != "" {
				host = u.Host
			}
		}
		b.WriteString("When a pull request on " + d.Source.Repo + " (" + host + ") ")
		switch {
		case opened && updated:
			b.WriteString("is opened or gets new commits")
		case opened:
			b.WriteString("is opened")
		default:
			b.WriteString("gets new commits")
		}
	case watch.Gmail:
		query := strings.TrimSpace(d.Source.Query)
		if query == "" {
			query = "in:inbox"
		}
		b.WriteString("When an email matching \"" + query + "\" ")
		switch {
		case opened && updated:
			b.WriteString("arrives or gets a reply in Gmail")
		case opened:
			b.WriteString("arrives in Gmail")
		default:
			b.WriteString("gets a reply in Gmail")
		}
	default:
		b.WriteString("When " + strings.Join(d.Source.Command, " ") + " prints ")
		switch {
		case opened && updated:
			b.WriteString("a new or changed item")
		case opened:
			b.WriteString("a new item")
		default:
			b.WriteString("a changed item")
		}
	}
	for i, f := range d.Filters {
		if i == 0 {
			b.WriteString(", if ")
		} else {
			b.WriteString(" and ")
		}
		b.WriteString(f.Words())
	}
	if d.Every == 1 {
		b.WriteString(", checked every minute")
	} else {
		fmt.Fprintf(&b, ", checked every %d minutes", d.Every)
	}
	if d.Routing == PerSubject {
		b.WriteString("; each in a task of its own")
	} else {
		b.WriteString("; all in one task")
	}
	switch {
	case d.Then.Post == "review" && d.Then.Auto:
		b.WriteString("; its answer is posted as a review without asking")
	case d.Then.Post == "review":
		b.WriteString("; its answer is held for you to post as a review")
	case d.Then.Post == "draft" && d.Then.Auto:
		b.WriteString("; its answer is saved as a draft reply without asking")
	case d.Then.Post == "draft":
		b.WriteString("; its answer is held for you to save as a draft reply")
	}
	switch d.Then.Notify {
	case "run":
		b.WriteString("; you are told after each run")
	case "never":
		b.WriteString("; you are not told")
	}
	return b.String()
}
