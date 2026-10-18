package core

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/redact"
	"github.com/fclaude/nodwyn/internal/window"
)

// A session log's index (ADR 49 §10) is what listing the session and reading
// a page of its log need, derived from the log and kept beside it as
// <id>.idx, so that neither reads the whole log again each time it grows: a
// task that runs every day for months keeps one log of gigabytes. It is a
// revocable view of the log (SPEC, invariant 1), and nothing is recorded only
// in it: a reader checks the line the index ends at, and every line it
// starts reading at, against the log, and builds the index again from the
// log on any mismatch, so deleting it is always safe. Logs only grow: their
// one repair, log.Open cutting a torn last line, cuts beyond the last
// complete event, which is as far as an index goes.
//
// It is written by whichever nodwyn process reads the log, after folding in
// what the log gained, as a temporary file renamed into place; two writers
// race harmlessly, since each wrote an index of the same log, and the one
// that covers less is folded forward by the next read. It lives in nodwyn's
// data directory, beside the log, where the model's file primitives cannot
// reach (the default denylist), and whatever could write it could write the
// log itself.

const indexVersion = 7

// markEvery is how many events apart the index notes where a line starts: a
// page is read from the nearest mark before it, at most this many events
// early.
const markEvery = 256

type logIndex struct {
	Version int `json:"version"`
	// The last event folded in: its seq, where its line starts and ends in
	// the log, and the SHA-256 of the line.
	Seq   uint64 `json:"seq"`
	Start int64  `json:"start"`
	End   int64  `json:"end"`
	Hash  string `json:"hash"`
	// Fold is the session's listing up to and including Seq.
	Fold infoFold `json:"fold"`
	// Resume is what resuming the session needs from the events it does
	// not hold, up to and including Seq.
	Resume   resumeFold   `json:"resume"`
	Totals   totalsFold   `json:"totals"`
	History  historyFold  `json:"history"`
	Schedule ScheduleInfo `json:"schedule"`
	Turns    []indexMark  `json:"turns,omitempty"`
	Runs     []indexMark  `json:"runs,omitempty"`
	// Marks[k] is where the line of event k*markEvery+1 starts, and
	// MarkSpend[k] what the requests before it cost, as knownSpend adds it.
	Marks     []int64   `json:"marks"`
	MarkSpend []float64 `json:"mark_spend"`
}

// resumeFold is what a resumed session needs from the part of its log it
// does not hold in memory (ADR 49 §10), folded as the listing is. A resumed
// session holds the events from base on: every reader of its events finds
// in them what it found in the whole log.
type resumeFold struct {
	// Window is where a window built now begins (window.StartTracker).
	Window window.StartTracker `json:"window"`
	// Requests are the seqs of the latest three user messages: the planner
	// reads two before the latest (plan-chat-request), the judge two
	// (earlierRequests), and request-context the turn before. Request is
	// the latest that is not a /compact, which a compaction hands over
	// (compact-last-request).
	Requests []uint64 `json:"requests,omitempty"`
	Request  uint64   `json:"request,omitempty"`
	// Finished is the seq of the latest turn.end of a turn that finished:
	// a plan an unfinished turn left is read from after it (unfinishedPlan).
	Finished uint64 `json:"finished,omitempty"`
	// RunStart is the seq of the latest run's first run.state: a run's plan
	// and state are read from there (runPlan, run.Restore).
	RunStart uint64 `json:"run_start,omitempty"`
	// Summaries says the log has a summary, which replaces spans a window's
	// start does not know of: such a log is held whole.
	Summaries bool `json:"summaries,omitempty"`
	// Spend is what the requests so far cost, as knownSpend adds it.
	Spend float64 `json:"spend,omitempty"`
	// Latest are the latest records of the choices a resumed task keeps,
	// wherever they are in the log: its model, judge, helpers and limits.
	Latest map[log.Kind]log.Event `json:"latest,omitempty"`
}

// keptChoices are the kinds whose latest record resumeFold keeps.
var keptChoices = []log.Kind{log.KindSessionModel, log.KindSessionJudge, log.KindSessionDelegates, log.KindSessionLimits, log.KindSessionSchedule, log.KindScheduleRun, log.KindContextHandover}

// observe folds one event in.
func (r *resumeFold) observe(ev log.Event) {
	r.Window.Observe(ev)
	if slices.Contains(keptChoices, ev.Kind) {
		if r.Latest == nil {
			r.Latest = map[log.Kind]log.Event{}
		}
		r.Latest[ev.Kind] = ev
	}
	switch ev.Kind {
	case log.KindUserMsg:
		var m log.UserMsg
		if ev.Decode(&m) != nil {
			return
		}
		r.Requests = append(r.Requests, ev.Seq)
		if len(r.Requests) > 3 {
			r.Requests = r.Requests[len(r.Requests)-3:]
		}
		if !compactRequest(m.Text) {
			r.Request = ev.Seq
		}
	case log.KindTurnEnd:
		var p log.TurnEnd
		if ev.Decode(&p) == nil && turnFinished(p.Outcome) {
			r.Finished = ev.Seq
		}
	case log.KindScheduleRun:
		r.RunStart = ev.Seq
	case log.KindRunState:
		var p log.RunState
		if ev.Decode(&p) == nil && p.Status == "running" && p.Turns == 0 {
			r.RunStart = ev.Seq
		}
	case log.KindSummary, log.KindSummaryDrop:
		r.Summaries = true
	case log.KindLLMResp, log.KindJevReq:
		if cost, ok := knownCost(ev); ok {
			r.Spend += cost
		}
	}
}

// base is the seq of the first event a resumed session holds: the earliest
// any of its readers needs.
func (r resumeFold) base() uint64 {
	if r.Summaries || len(r.Requests) == 0 {
		return 1
	}
	b := min(r.Window.Start(), r.Requests[0], r.Finished+1)
	if r.Request > 0 {
		b = min(b, r.Request)
	}
	if r.RunStart > 0 {
		b = min(b, r.RunStart)
	}
	return max(b, 1)
}

// choices are the latest choice records, oldest first, for the readers that
// take the latest of a kind from a list of events.
func (r resumeFold) choices() []log.Event {
	var out []log.Event
	for _, ev := range r.Latest {
		out = append(out, ev)
	}
	slices.SortFunc(out, func(a, b log.Event) int { return cmp.Compare(a.Seq, b.Seq) })
	return out
}

// compactRequest is compact.scm's compact-request?: a message that is a
// /compact command.
func compactRequest(text string) bool {
	r := strings.TrimSpace(text)
	return r == "/compact" || strings.HasPrefix(r, "/compact ") || strings.HasPrefix(r, "/compact\n")
}

// turnFinished is unfinishedPlan's: a turn finished unless an error, a
// limit or the person's Stop ended it.
func turnFinished(outcome string) bool {
	switch outcome {
	case "error", "stopped", "hard_budget", "spend_limit", "canceled":
		return false
	}
	return true
}

func indexPath(dir, id string) string { return filepath.Join(dir, id+".idx") }

// index is the session's index brought to the end of its log: <id>.idx when
// it still matches the log, else one built from the start, with the events
// the log gained since folded in, and written back when it grew. truncated
// reports a partial last line, which no index covers.
func (s Store) index(id string) (logIndex, bool, error) {
	path, err := s.path(id)
	if err != nil {
		return logIndex{}, false, err
	}
	f, err := os.Open(path)
	if err != nil {
		return logIndex{}, false, fmt.Errorf("log: %w", err)
	}
	defer f.Close()
	idx, loaded := loadIndex(indexPath(s.Dir, id), f)
	if !loaded {
		idx = freshIndex(id)
	}
	result, truncated, err := extendIndex(f, id, idx)
	if err != nil && loaded {
		// The log does not go on from where the index says it ends: the
		// index is not this log's. Built again from the start, the log
		// says whether it is the log that is wrong.
		result, truncated, err = extendIndex(f, id, freshIndex(id))
	}
	if err != nil {
		return logIndex{}, false, err
	}
	if _, err := result.Fold.result(id); err != nil {
		return logIndex{}, false, err
	}
	if result.Seq > idx.Seq {
		// Best effort: a directory nodwyn cannot write to only means the
		// next read folds the same events in again.
		_ = saveIndex(s.Dir, id, result)
	}
	return result, truncated, nil
}

func freshIndex(id string) logIndex {
	return logIndex{Version: indexVersion, Fold: infoFold{Info: SessionInfo{ID: id}}}
}

// extendIndex folds in the events of f after the ones idx covers.
func extendIndex(f *os.File, id string, idx logIndex) (logIndex, bool, error) {
	if _, err := f.Seek(idx.End, io.SeekStart); err != nil {
		return logIndex{}, false, err
	}
	base, previous := idx.End, int64(0)
	err := log.ReplayFrom(f, idx.Seq+1, func(ev log.Event, line []byte, end int64) error {
		// A line starts where the one before it ended: a blank line between
		// them is skipped by whoever reads from there.
		start := base + previous
		previous = end
		if (ev.Seq-1)%markEvery == 0 {
			idx.Marks = append(idx.Marks, start)
			idx.MarkSpend = append(idx.MarkSpend, idx.Resume.Spend)
		}
		if err := idx.Fold.observe(id, ev); err != nil {
			return err
		}
		idx.Resume.observe(ev)
		idx.observeTotals(ev, start)
		idx.observeHistory(ev)
		idx.Schedule.observe(ev)
		idx.Seq, idx.Start, idx.End, idx.Hash = ev.Seq, start, base+end, lineHash(line)
		return nil
	})
	truncated := errors.Is(err, log.ErrTruncated)
	if err != nil && !truncated {
		return logIndex{}, false, err
	}
	return idx, truncated, nil
}

// loadIndex reads the index at path when it matches the log f: the line it
// says it ends at is in f, whole, with its seq and hash. Anything else is
// no index.
func loadIndex(path string, f *os.File) (logIndex, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return logIndex{}, false
	}
	var idx logIndex
	if json.Unmarshal(raw, &idx) != nil || idx.Version != indexVersion || idx.Seq == 0 || idx.Start < 0 || idx.End <= idx.Start ||
		uint64(len(idx.Marks)) != (idx.Seq-1)/markEvery+1 || len(idx.MarkSpend) != len(idx.Marks) || idx.Marks[0] != 0 {
		return logIndex{}, false
	}
	if !lineMatches(f, idx.Seq, idx.Start, idx.End, idx.Hash) {
		return logIndex{}, false
	}
	return idx, true
}

// lineMatches reports whether f holds, from start to end, the whole line of
// event seq whose SHA-256 is hash: what a file derived from a log checks
// before it trusts that it is that log's.
func lineMatches(f *os.File, seq uint64, start, end int64, hash string) bool {
	if start < 0 || end <= start {
		return false
	}
	line := make([]byte, end-start)
	if _, err := f.ReadAt(line, start); err != nil || line[len(line)-1] != '\n' {
		return false
	}
	line = bytes.TrimSpace(line)
	sum := sha256.Sum256(line)
	var ev log.Event
	return hex.EncodeToString(sum[:]) == hash && json.Unmarshal(line, &ev) == nil && ev.Seq == seq
}

// lineHash is the SHA-256 lineMatches compares.
func lineHash(line []byte) string {
	sum := sha256.Sum256(line)
	return hex.EncodeToString(sum[:])
}

// saveIndex writes idx beside the log, whole or not at all.
func saveIndex(dir, id string, idx logIndex) error { return writeDerived(dir, id, ".idx", idx) }

// writeDerived writes v as JSON beside session id's log, as <id><ext>, whole
// or not at all.
func writeDerived(dir, id, ext string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return writeDerivedBytes(dir, id, ext, raw)
}

// writeDerivedBytes writes raw beside session id's log, as <id><ext>, whole
// or not at all: a temporary file renamed into place.
func writeDerivedBytes(dir, id, ext string, raw []byte) error {
	tmp, err := os.CreateTemp(dir, id+ext+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, id+ext))
}

// errPageFull ends a page's read once it holds what it asked for.
var errPageFull = errors.New("core: page full")

// readPage reads a page of the log from the mark before it: the events after
// after, through through, at most limit of them, redacted for display. A
// mark that is not where its event's line starts is an index that is not
// this log's: it is removed, and the caller reads again.
func (s Store) readPage(id string, idx logIndex, page *LogPage, after, through uint64, limit int) error {
	k := after / markEvery
	seq, offset := k*markEvery+1, idx.Marks[k]
	// Turn and run boundaries shorten reads that begin between sparse marks.
	for _, marks := range [][]indexMark{idx.Turns, idx.Runs} {
		i, _ := slices.BinarySearchFunc(marks, after+1, func(m indexMark, n uint64) int { return cmp.Compare(m.Seq, n) })
		if i == len(marks) || marks[i].Seq > after+1 {
			i--
		}
		if i >= 0 && marks[i].Seq > seq {
			seq, offset = marks[i].Seq, marks[i].Offset
		}
	}
	path, err := s.path(id)
	if err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("log: %w", err)
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		os.Remove(indexPath(s.Dir, id))
		return errStaleIndex
	}
	read := false
	err = log.ReplayFrom(f, seq, func(ev log.Event, _ []byte, _ int64) error {
		read = true
		if ev.Seq <= after {
			return nil
		}
		if ev.Seq > through || len(page.Events) == limit {
			return errPageFull
		}
		if len(ev.Payload) > 0 {
			var err error
			if ev.Payload, _, err = redact.JSON(ev.Payload, nil); err != nil {
				return err
			}
		}
		page.Events = append(page.Events, ev)
		page.Next = ev.Seq
		return nil
	})
	if !read {
		// Not even the mark's own line was there.
		os.Remove(indexPath(s.Dir, id))
		return errStaleIndex
	}
	if err != nil && !errors.Is(err, errPageFull) && !errors.Is(err, log.ErrTruncated) {
		return err
	}
	return nil
}

// suffix reads the events a resumed session holds (resumeFold.base), from
// the mark at or before the first of them to the end of the log, and what
// the requests before them cost. A mark that is not where its event's line
// starts is an index that is not this log's: it is removed, and the whole
// log is read instead.
func (s Store) suffix(id string, idx logIndex) ([]log.Event, float64, error) {
	k := (idx.Resume.base() - 1) / markEvery
	path, err := s.path(id)
	if err != nil {
		return nil, 0, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, fmt.Errorf("log: %w", err)
	}
	defer f.Close()
	if _, err := f.Seek(idx.Marks[k], io.SeekStart); err != nil {
		return nil, 0, err
	}
	var events []log.Event
	err = log.ReplayFrom(f, k*markEvery+1, func(ev log.Event, _ []byte, _ int64) error {
		events = append(events, ev)
		return nil
	})
	if err != nil && !errors.Is(err, log.ErrTruncated) {
		if len(events) > 0 {
			return nil, 0, err
		}
		os.Remove(indexPath(s.Dir, id))
		_, all, err := s.read(id)
		return all, 0, err
	}
	return events, idx.MarkSpend[k], nil
}

// errStaleIndex says an index's mark did not match its log.
var errStaleIndex = errors.New("core: the log's index did not match it")
