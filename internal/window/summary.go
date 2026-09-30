package window

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/redact"
)

// MaxSummaryBytes bounds a summary's text.
const MaxSummaryBytes = 16 * 1024

// SummarySourceHash validates whole conversational turns and fingerprints their
// redacted source events. Tool-call batches may not be cut in half.
func SummarySourceHash(events []log.Event, from, through uint64) (string, error) {
	if from == 0 || through <= from {
		return "", fmt.Errorf("summary: invalid span")
	}
	first, last := -1, -1
	for i, ev := range events {
		if ev.Seq == from {
			first = i
		}
		if ev.Seq == through {
			last = i
		}
	}
	if first < 0 || last < first || events[first].Kind != log.KindUserMsg || events[last].Kind != log.KindLLMResp {
		return "", fmt.Errorf("summary: span must start at user.msg and end at a completed llm.resp")
	}
	var final log.LLMResp
	if err := events[last].Decode(&final); err != nil || len(final.ToolCalls) != 0 {
		return "", fmt.Errorf("summary: span ends with incomplete model/tool work")
	}
	sum := sha256.New()
	encoder := json.NewEncoder(sum)
	for _, ev := range events[first : last+1] {
		// Summaries are caches of the conversation, never of other summaries or
		// approval records. Original tool-call and result IDs stay in the source.
		switch ev.Kind {
		case log.KindUserMsg, log.KindHarnessMsg, log.KindLLMResp, log.KindToolResult:
		default:
			continue
		}
		raw, _, err := redact.JSON(ev.Payload, nil)
		if err != nil {
			return "", err
		}
		if err := encoder.Encode(struct {
			Seq     uint64
			Kind    log.Kind
			Payload json.RawMessage
		}{ev.Seq, ev.Kind, raw}); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}

// SummaryInfo reports a cache's current status without changing the log.
type SummaryInfo struct {
	Seq     uint64      `json:"seq"`
	Summary log.Summary `json:"summary"`
	Status  string      `json:"status"` // active, recent, dropped, stale, superseded
}

// Summaries lists every summary in the log with its status: whether the
// window uses it, and if not, why (dropped, stale, superseded, or too
// recent to replace).
func Summaries(events []log.Event) []SummaryInfo {
	out := []SummaryInfo{}
	dropped := map[uint64]bool{}
	var turns []uint64
	for _, ev := range events {
		switch ev.Kind {
		case log.KindUserMsg:
			turns = append(turns, ev.Seq)
		case log.KindSummary:
			var p log.Summary
			if ev.Decode(&p) == nil {
				out = append(out, SummaryInfo{Seq: ev.Seq, Summary: p})
			}
		case log.KindSummaryDrop:
			var p log.SummaryDrop
			if ev.Decode(&p) == nil && p.SummarySeq < ev.Seq {
				dropped[p.SummarySeq] = true
			}
		}
	}
	cutoff := uint64(0)
	if len(turns) >= 2 {
		cutoff = turns[len(turns)-2]
	}
	var newer []log.Summary
	for i := len(out) - 1; i >= 0; i-- {
		info := &out[i]
		p := info.Summary
		hash, err := SummarySourceHash(events, p.From, p.Through)
		if err != nil || p.Through >= info.Seq {
			info.Status = "stale"
			continue
		}
		overlap := false
		for _, prior := range newer {
			if p.From <= prior.Through && prior.From <= p.Through {
				overlap = true
				break
			}
		}
		newer = append(newer, p)
		switch {
		case dropped[info.Seq]:
			info.Status = "dropped"
		case overlap:
			info.Status = "superseded"
		case hash != p.SourceHash || strings.TrimSpace(p.Text) == "" || len(p.Text) > MaxSummaryBytes:
			info.Status = "stale"
		case p.Through >= cutoff:
			info.Status = "recent"
		default:
			info.Status = "active"
		}
	}
	return out
}

// summaryWindow replaces only older, complete spans. A revoked newer summary
// never revives an older overlapping cache. Lifecycle edits invalidate derived
// elision decisions; the normal budget policy then recomputes them from source.
func summaryWindow(events []log.Event) []log.Event {
	summaries := Summaries(events)
	var active []SummaryInfo
	for _, info := range summaries {
		if info.Status == "active" {
			active = append(active, info)
		}
	}
	barrier := uint64(0)
	for _, ev := range events {
		if ev.Kind == log.KindSummary || ev.Kind == log.KindSummaryDrop {
			barrier = ev.Seq
		}
	}
	if len(active) == 0 && barrier == 0 {
		return events
	}
	out := make([]log.Event, 0, len(events))
	for _, ev := range events {
		if ev.Kind == log.KindLLMReq && ev.Seq < barrier {
			continue
		}
		covered := false
		for _, info := range active {
			p := info.Summary
			if ev.Seq < p.From || ev.Seq > p.Through {
				continue
			}
			if ev.Seq == p.From {
				text := fmt.Sprintf("Summary of earlier conversation (summary seq=%d, source=%d..%d; revocable):\n%s", info.Seq, p.From, p.Through, redact.Redact(p.Text, nil).Text)
				payload, _ := json.Marshal(log.LLMResp{Content: text})
				out = append(out, log.Event{Seq: info.Seq, Kind: log.KindLLMResp, Payload: payload})
			}
			// Preserve non-conversation events needed for the policy fold.
			switch ev.Kind {
			case log.KindUserMsg, log.KindHarnessMsg, log.KindLLMResp, log.KindToolResult, log.KindGatePlan:
				covered = true
			}
			break
		}
		if !covered {
			out = append(out, ev)
		}
	}
	return out
}
