package core

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/fclaude/nibb/internal/log"
	"github.com/fclaude/nibb/internal/redact"
	"github.com/fclaude/nibb/internal/window"
)

// Summaries inspects revocable memory from a validated session log.
func (s Store) Summaries(id string) ([]window.SummaryInfo, error) {
	info, events, err := s.read(id)
	if err != nil {
		return nil, err
	}
	if info.Truncated {
		return nil, fmt.Errorf("summary: resume the session to recover its incomplete log first")
	}
	views := window.Summaries(events)
	clean, _, err := redact.Payload(views, nil)
	if err != nil {
		return nil, err
	}
	return clean.([]window.SummaryInfo), nil
}

// AddSummary records user-supplied text for an explicitly chosen source span.
// This is a local user operation, not a Scheme primitive or a model tool.
func (s Store) AddSummary(id string, from, through uint64, text string) (log.Event, error) {
	if len(text) > window.MaxSummaryBytes {
		return log.Event{}, fmt.Errorf("summary: text exceeds %d bytes", window.MaxSummaryBytes)
	}
	if !utf8.ValidString(text) {
		return log.Event{}, fmt.Errorf("summary: text must be valid UTF-8")
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return log.Event{}, fmt.Errorf("summary: text must contain 1..%d bytes", window.MaxSummaryBytes)
	}
	return s.recordSummary(id, log.Summary{From: from, Through: through, Text: text}, 0)
}

// DropSummary revokes the summary recorded at seq; the window falls back to
// its source events. Like AddSummary, a local user operation.
func (s Store) DropSummary(id string, seq uint64) (log.Event, error) {
	if seq == 0 {
		return log.Event{}, fmt.Errorf("summary: a positive summary sequence is required")
	}
	return s.recordSummary(id, log.Summary{}, seq)
}

// Summary edits own the existing log exclusively, without constructing an
// interpreter or replaying initialization. Append uses the log's mandatory
// redaction boundary. There is no in-memory session history to mutate here.
func (s Store) recordSummary(id string, summary log.Summary, drop uint64) (log.Event, error) {
	path, err := s.path(id)
	if err != nil {
		return log.Event{}, err
	}
	info, _, err := s.read(id)
	if err != nil {
		return log.Event{}, err
	}
	if info.Truncated {
		return log.Event{}, fmt.Errorf("summary: resume the session to recover its incomplete log first")
	}
	writer, err := log.Open(path)
	if err != nil {
		if writer != nil {
			writer.Close()
		}
		return log.Event{}, err
	}
	defer writer.Close()
	_, events, err := s.read(id)
	if err != nil {
		return log.Event{}, err
	}
	if drop != 0 {
		found := false
		for _, ev := range events {
			if ev.Seq == drop && ev.Kind == log.KindSummary {
				found = true
			}
			if ev.Kind == log.KindSummaryDrop {
				var p log.SummaryDrop
				if ev.Decode(&p) == nil && p.SummarySeq == drop {
					return log.Event{}, fmt.Errorf("summary: seq %d is already revoked", drop)
				}
			}
		}
		if !found {
			return log.Event{}, fmt.Errorf("summary: no summary at seq %d", drop)
		}
		return writer.Append(log.KindSummaryDrop, log.SummaryDrop{SummarySeq: drop})
	}
	summary.SourceHash, err = window.SummarySourceHash(events, summary.From, summary.Through)
	if err != nil {
		return log.Event{}, err
	}
	return writer.Append(log.KindSummary, summary)
}
