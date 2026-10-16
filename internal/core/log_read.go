package core

import (
	"errors"
	"fmt"

	"github.com/fclaude/nodwyn/internal/log"
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
	Run       *LogRun     `json:"run,omitempty"`
	Earlier   bool        `json:"earlier,omitempty"`
	Context   []log.Event `json:"context,omitempty"`
}

// ReadLog never executes or rewrites history. A zero through selects the last
// complete record; limit is 1..1000. Legacy payloads are redacted for display.
// A partial final line is reported without discarding complete records. The
// page is read from the log's index (log_index.go): from the nearest mark
// before it, not from the log's first event.
func (s Store) ReadLog(id string, after, through uint64, limit int) (LogPage, error) {
	return s.readLog(id, after, through, limit, func(id string, _ bool) (logIndex, bool, error) { return s.index(id) })
}

// readLog is ReadLog with where the index comes from: indexOf, which is
// told when the one it gave before was stale and must be built again.
func (s Store) readLog(id string, after, through uint64, limit int, indexOf func(id string, fresh bool) (logIndex, bool, error)) (LogPage, error) {
	if limit < 1 || limit > 1000 {
		return LogPage{}, fmt.Errorf("core: log limit must be between 1 and 1000")
	}
	for attempt := 0; ; attempt++ {
		idx, truncated, err := indexOf(id, attempt > 0)
		if err != nil {
			return LogPage{}, err
		}
		last := idx.Seq
		if through == 0 {
			through = last
		}
		if through > last || after > through {
			return LogPage{}, fmt.Errorf("core: log cursor is beyond the available history")
		}
		page := LogPage{Events: []log.Event{}, Next: after, Through: through, Truncated: truncated}
		if after < through {
			err = s.readPage(id, idx, &page, after, through, limit)
		}
		// An index whose mark did not match its log was removed: the next
		// one is built from the log.
		if errors.Is(err, errStaleIndex) && attempt == 0 {
			continue
		}
		if err != nil {
			return LogPage{}, err
		}
		page.More = page.Next < through
		return page, nil
	}
}
