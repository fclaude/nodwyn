package plain

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/fclaude/nodwyn/internal/log"
)

// Retrieval's lines (ADR 50), shared by line mode and the TUI as the
// console's transcript.js says them.

// searched says where a search with a scope looked.
var searched = map[string]string{"repo": "in the repository", "conversation": "in this conversation", "earlier": "in earlier tasks"}

// CallSummary is a call's arguments in words, for the tools whose arguments
// read badly as values: a search says what it searched for and where, a
// recall the event and its task, an index update what it indexes. ok is
// false for any other tool, whose arguments are shown as they are.
func CallSummary(name string, args json.RawMessage) (string, bool) {
	var a struct {
		Query string `json:"query"`
		Scope string `json:"scope"`
		Path  string `json:"path"`
		Seq   *int64 `json:"seq"`
		Start int64  `json:"start"`
		Task  string `json:"task"`
	}
	if len(args) > 0 && json.Unmarshal(args, &a) != nil {
		return "", false
	}
	switch name {
	case "index-search":
		where := searched[a.Scope]
		if where == "" {
			where = a.Scope
		}
		out := "“" + a.Query + "” " + where
		if a.Path != "" {
			out += " under " + a.Path
		}
		return out, true
	case "recall":
		if a.Seq == nil {
			return "", false
		}
		out := fmt.Sprintf("seq %d", *a.Seq)
		if a.Task != "" {
			out += " of task " + a.Task
		}
		if a.Start > 0 {
			out += fmt.Sprintf(" from line %d", a.Start)
		}
		return out, true
	case "index-update":
		if a.Path != "" {
			return "adding " + a.Path, true
		}
		return "the whole repository", true
	}
	return "", false
}

// Indexing reports whether a tool builds or refreshes the repository's
// index, reading every file it needs through the guard: those reads are
// its own, summed by its index.update, not each worth a line.
func Indexing(tool string) bool { return tool == "index-search" || tool == "index-update" }

// IndexUpdateLine is the one line a transcript shows for a build or refresh
// of the repository's index: what it read, removed and skipped, the files
// the guard left out, what it had no time for, and what it holds.
func IndexUpdateLine(u log.IndexUpdate) string {
	files := func(n int) string {
		if n == 1 {
			return "1 file"
		}
		return fmt.Sprintf("%d files", n)
	}
	parts := []string{"read " + files(u.Read)}
	if u.Removed > 0 {
		parts = append(parts, fmt.Sprintf("%d removed", u.Removed))
	}
	if u.Skipped > 0 {
		parts = append(parts, fmt.Sprintf("%d skipped", u.Skipped))
	}
	if len(u.Refused) > 0 {
		parts = append(parts, "left out by the guard: "+strings.Join(u.Refused, ", "))
	}
	if u.Unread > 0 {
		parts = append(parts, files(u.Unread)+" still to read")
	}
	if u.Partial {
		parts = append(parts, "the repository is larger than an index holds")
	}
	return fmt.Sprintf("[index updated: %s · holds %s · %.1f s]", strings.Join(parts, " · "), files(u.Files), float64(u.Millis)/1000)
}
