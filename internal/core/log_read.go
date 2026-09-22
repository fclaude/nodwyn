package core

import (
	"fmt"

	"github.com/fclaude/nibb/internal/log"
	"github.com/fclaude/nibb/internal/redact"
)

// LogPage is a bounded response over a fixed durable prefix. Through is the
// snapshot's last sequence; Next is the last returned sequence (or After for
// an empty page). Reuse Through on subsequent pages so a growing log cannot
// keep moving the end of a reconnect catch-up.
type LogPage struct {
	Events    []log.Event `json:"events"`
	Next      uint64      `json:"next"`
	Through   uint64      `json:"through"`
	More      bool        `json:"more"`
	Truncated bool        `json:"truncated"`
}

// ReadLog never executes or rewrites history. A zero through selects the last
// complete record; limit is 1..1000. Legacy payloads are redacted for display.
// A partial final line is reported without discarding complete records.
func (s Store) ReadLog(id string, after, through uint64, limit int) (LogPage, error) {
	if limit < 1 || limit > 1000 {
		return LogPage{}, fmt.Errorf("core: log limit must be between 1 and 1000")
	}
	info, history, err := s.read(id)
	if err != nil {
		return LogPage{}, err
	}
	last := history[len(history)-1].Seq
	if through == 0 {
		through = last
	}
	if through > last || after > through {
		return LogPage{}, fmt.Errorf("core: log cursor is beyond the available history")
	}
	page := LogPage{Events: []log.Event{}, Next: after, Through: through, Truncated: info.Truncated}
	for _, ev := range history {
		if ev.Seq <= after {
			continue
		}
		if ev.Seq > through || len(page.Events) == limit {
			break
		}
		if len(ev.Payload) > 0 {
			ev.Payload, _, err = redact.JSON(ev.Payload, nil)
			if err != nil {
				return LogPage{}, err
			}
		}
		page.Events = append(page.Events, ev)
		page.Next = ev.Seq
	}
	page.More = page.Next < through
	return page, nil
}
