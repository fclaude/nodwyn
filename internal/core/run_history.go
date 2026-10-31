package core

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/redact"
)

// LogRun is a bounded preview of a run, not another copy of its transcript.
// Through fixes its inclusive end even while the session gains more records.
type LogRun struct {
	SessionMark
	Through uint64 `json:"through"`
	Reply   string `json:"reply"`
}

type RunPage struct {
	Runs      []LogRun `json:"runs"`
	Through   uint64   `json:"through"`
	Before    uint64   `json:"before"`
	More      bool     `json:"more"`
	Truncated bool     `json:"truncated"`
}

type historyFold struct {
	Context map[log.Kind]log.Event `json:"context,omitempty"`
	Intro   indexMark              `json:"intro"`
}

// observeHistory keeps previews and the setup a run inherits, so opening it
// does not need a read of the conversation before its boundary.
func (idx *logIndex) observeHistory(ev log.Event) {
	h := &idx.History
	if ev.Seq == 1 {
		h.Intro.SessionMark = SessionMark{Seq: 1, TS: ev.TS}
	}
	switch ev.Kind {
	case log.KindSessionStart, log.KindSessionResume, log.KindSessionModel, log.KindSessionJudge, log.KindSessionDelegates, log.KindSessionLimits, log.KindSessionCommands:
		if h.Context == nil {
			h.Context = map[log.Kind]log.Event{}
		}
		h.Context[ev.Kind] = ev
	}
	if n := len(idx.Runs); n > 0 && idx.Runs[n-1].Seq == ev.Seq {
		if n > 1 {
			for i := range idx.Runs[n-2].Context {
				idx.Runs[n-2].Context[i].Payload = nil
			}
		}
		for _, e := range h.Context {
			idx.Runs[n-1].Context = append(idx.Runs[n-1].Context, e)
		}
		slices.SortFunc(idx.Runs[n-1].Context, func(a, b log.Event) int { return cmp.Compare(a.Seq, b.Seq) })
	}
	if ev.Kind == log.KindLLMResp {
		var p log.LLMResp
		if ev.Decode(&p) == nil && strings.TrimSpace(p.Content) != "" {
			m := &h.Intro
			if len(idx.Runs) > 0 {
				m = &idx.Runs[len(idx.Runs)-1]
			}
			// Redact before clipping so a preview cannot expose the beginning of
			// a legacy secret cut short at the preview boundary.
			text := []rune(redact.Redact(p.Content, nil).Text)
			if len(text) > 2000 {
				text = append(text[:2000], '…')
			}
			m.Reply, m.ReplySeq = string(text), ev.Seq
		}
	}
}

// historyMarks includes conversation before the first run when it contains
// work, rather than exposing a separate empty run for startup records alone.
func (idx logIndex) historyMarks(through uint64) []indexMark {
	n, _ := slices.BinarySearchFunc(idx.Runs, through+1, func(m indexMark, seq uint64) int { return cmp.Compare(m.Seq, seq) })
	if n == 0 {
		return []indexMark{idx.History.Intro}
	}
	marks := slices.Clone(idx.Runs[:n])
	if len(idx.Turns) > 0 && idx.Turns[0].Seq < marks[0].Seq {
		marks = append([]indexMark{idx.History.Intro}, marks...)
	} else {
		marks[0].Seq, marks[0].Offset, marks[0].Context = 1, 0, nil
	}
	return marks
}

func runPreview(m indexMark, through uint64) LogRun {
	r := LogRun{SessionMark: m.SessionMark, Through: through}
	if m.ReplySeq <= through {
		r.Reply = redact.Redact(m.Reply, nil).Text
	}
	return r
}

// ReadRuns pages backwards over a fixed prefix, newest first, with at most
// 100 previews. Before is an exclusive start-sequence cursor, not an offset.
func (s Store) ReadRuns(id string, before, through uint64, limit int) (RunPage, error) {
	if limit < 1 || limit > 100 {
		return RunPage{}, fmt.Errorf("core: run limit must be between 1 and 100")
	}
	idx, truncated, err := s.index(id)
	if err != nil {
		return RunPage{}, err
	}
	if through == 0 {
		through = idx.Seq
	}
	if through > idx.Seq || before > through {
		return RunPage{}, fmt.Errorf("core: run cursor is beyond the available history")
	}
	marks := idx.historyMarks(through)
	end := len(marks)
	if before > 0 {
		var found bool
		end, found = slices.BinarySearchFunc(marks, before, func(m indexMark, seq uint64) int { return cmp.Compare(m.Seq, seq) })
		if !found {
			return RunPage{}, fmt.Errorf("core: before must name a run boundary")
		}
	}
	page := RunPage{Runs: []LogRun{}, Through: through, Truncated: truncated, Before: before}
	start := max(0, end-limit)
	for i := end - 1; i >= start; i-- {
		until := through
		if i+1 < len(marks) {
			until = marks[i+1].Seq - 1
		}
		page.Runs = append(page.Runs, runPreview(marks[i], until))
		page.Before = marks[i].Seq
	}
	page.More = start > 0
	return page, nil
}

// ReadLatestRun shares ReadLog's fixed-prefix paging, selecting its first
// cursor from the index. Subscription calls it while durable delivery is
// paused, so there is no gap between the suffix and subsequent live records.
func (s Store) ReadLatestRun(id string, through uint64, limit int) (LogPage, error) {
	if limit < 1 || limit > 1000 {
		return LogPage{}, fmt.Errorf("core: log limit must be between 1 and 1000")
	}
	read := s.eventReader()
	for attempt := 0; ; attempt++ {
		idx, truncated, err := s.index(id)
		if err != nil {
			return LogPage{}, err
		}
		read.indexes[id] = cachedIndex{idx, truncated} // the context events below are read through it
		if through == 0 {
			through = idx.Seq
		}
		if through > idx.Seq {
			return LogPage{}, fmt.Errorf("core: log cursor is beyond the available history")
		}
		marks := idx.historyMarks(through)
		m := marks[len(marks)-1]
		run := runPreview(m, through)
		page := LogPage{Events: []log.Event{}, Next: m.Seq - 1, Through: through, Truncated: truncated, Run: &run}
		if m.Seq > 1 {
			page.Context = slices.Clone(m.Context)
			for i, ev := range page.Context {
				// An earlier run's setup is read from the log.
				if ev.Payload == nil {
					if ev, err = read.at(id, ev.Seq); err != nil {
						return LogPage{}, err
					}
				}
				if ev.Payload, _, err = redact.JSON(ev.Payload, nil); err != nil {
					return LogPage{}, err
				}
				page.Context[i] = ev
			}
		}
		err = s.readPage(id, idx, &page, page.Next, through, limit)
		if errors.Is(err, errStaleIndex) && attempt == 0 {
			continue
		}
		if err != nil {
			return LogPage{}, err
		}
		page.More = page.Next < through
		page.Earlier = len(marks) > 1
		return page, nil
	}
}
