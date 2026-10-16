package core

import (
	"cmp"
	"errors"
	"fmt"
	"slices"

	"github.com/fclaude/nodwyn/internal/log"
)

// LogContext fixes the turn and run boundaries around an event. Page starts
// with at most 81 records around it; ReadLog can page either way inside From
// and Through. This never resumes a task or applies its historical choices.
type LogContext struct {
	From    uint64       `json:"from"`
	Through uint64       `json:"through"`
	Run     SessionMark  `json:"run"`
	Turn    *SessionMark `json:"turn,omitempty"`
	Page    LogPage      `json:"page"`
}

func (s Store) ReadContext(id string, seq uint64) (LogContext, error) {
	for attempt := 0; ; attempt++ {
		idx, truncated, err := s.index(id)
		if err != nil {
			return LogContext{}, err
		}
		if seq == 0 || seq > idx.Seq {
			return LogContext{}, fmt.Errorf("seq must name an available event")
		}
		runs := idx.historyMarks(idx.Seq)
		n, _ := slices.BinarySearchFunc(runs, seq+1, func(m indexMark, n uint64) int { return cmp.Compare(m.Seq, n) })
		run := runs[n-1]
		end := idx.Seq
		if n < len(runs) {
			end = runs[n].Seq - 1
		}
		result := LogContext{From: run.Seq, Through: end, Run: run.SessionMark}
		turn, _ := slices.BinarySearchFunc(idx.Turns, seq+1, func(m indexMark, n uint64) int { return cmp.Compare(m.Seq, n) })
		if turn > 0 && idx.Turns[turn-1].Seq >= run.Seq {
			mark := idx.Turns[turn-1].SessionMark
			result.Turn = &mark
			result.From = mark.Seq
		}
		if turn < len(idx.Turns) {
			result.Through = min(result.Through, idx.Turns[turn].Seq-1)
		}
		after := result.From - 1
		if seq > 40 {
			after = max(after, seq-41)
		}
		through := result.Through
		if result.Through-seq > 40 {
			through = seq + 40
		}
		result.Page = LogPage{Events: []log.Event{}, Next: after, Through: through, Truncated: truncated}
		err = s.readPage(id, idx, &result.Page, after, through, 81)
		if errors.Is(err, errStaleIndex) && attempt == 0 {
			continue
		}
		if err != nil {
			return LogContext{}, err
		}
		result.Page.More = result.Page.Next < through
		return result, nil
	}
}
