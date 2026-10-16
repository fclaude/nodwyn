package core

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/fclaude/nodwyn/internal/redact"
	"github.com/fclaude/nodwyn/internal/retrieval"
)

type ConversationLine struct {
	Number int    `json:"number"`
	Text   string `json:"text"`
}
type ConversationMatch struct {
	Seq     uint64             `json:"seq"`
	Kind    string             `json:"kind"`
	Time    time.Time          `json:"time"`
	Heading string             `json:"heading"`
	Lines   []ConversationLine `json:"lines"`
}
type ConversationTurn struct {
	Heading string              `json:"heading"`
	Matches []ConversationMatch `json:"matches"`
}
type ConversationTask struct {
	ID       string             `json:"id"`
	Title    string             `json:"title"`
	Archived bool               `json:"archived"`
	Turns    []ConversationTurn `json:"turns"`
}
type ConversationSearch struct {
	Tasks          []ConversationTask `json:"tasks"`
	SearchedTasks  int                `json:"searched_tasks"`
	SearchedEvents int                `json:"searched_events"`
	Unavailable    int                `json:"unavailable"`
}

// SearchConversations is a person's search of their logs. The anchor's log
// supplies the project; no repository file or client-supplied path grants
// access to another project. The model's earlier-tasks switch still governs
// the model alone, as it does for the memory CLI (ADR 51).
func (s Store) SearchConversations(id, scope, query string, limit int) (ConversationSearch, error) {
	result := ConversationSearch{Tasks: []ConversationTask{}}
	if limit < 1 || limit > 20 {
		return result, fmt.Errorf("search limit must be between 1 and 20")
	}
	if len(query) > 4096 || strings.TrimSpace(query) == "" || len(retrieval.Words(query)) == 0 {
		return result, fmt.Errorf("the query needs words and must be at most 4096 bytes")
	}
	anchor, err := s.Info(id)
	if err != nil {
		return result, err
	}
	tasks := []SessionInfo{anchor}
	switch scope {
	case "conversation":
	case "earlier":
		tasks, err = s.projectTasks(anchor.Project(), id)
		if err != nil {
			return result, err
		}
	default:
		return result, fmt.Errorf("scope must be conversation or earlier")
	}
	count := len(tasks)
	tasks, logs := s.memories(tasks, queryWords(query))
	result.SearchedTasks, result.Unavailable = len(tasks), count-len(tasks)
	groups, events := rankTurns(logs, query, limit)
	result.SearchedEvents = events
	positions := map[int]int{}
	read := s.eventReader()
	for _, g := range groups {
		turn := ConversationTurn{Heading: redact.Redact(g.head, nil).Text, Matches: []ConversationMatch{}}
		for _, hit := range g.hits {
			ev, err := read.at(tasks[g.log].ID, hit.doc.Seq)
			if err != nil {
				result.Unavailable++
				continue
			}
			match := ConversationMatch{Seq: ev.Seq, Kind: string(ev.Kind), Time: ev.TS, Heading: redact.Redact(logs[g.log].heading(hit.doc), nil).Text, Lines: []ConversationLine{}}
			for _, line := range bestLines(eventText(ev), hit.matched) {
				match.Lines = append(match.Lines, ConversationLine{Number: line.n, Text: conversationSnippet(redact.Redact(line.text, nil).Text, hit.matched)})
			}
			turn.Matches = append(turn.Matches, match)
		}
		if len(turn.Matches) == 0 {
			continue
		}
		at, ok := positions[g.log]
		if !ok {
			at = len(result.Tasks)
			positions[g.log] = at
			info := tasks[g.log]
			result.Tasks = append(result.Tasks, ConversationTask{ID: info.ID, Title: cut(redact.Redact(info.Title(), nil).Text, 160), Archived: info.Archived, Turns: []ConversationTurn{}})
		}
		result.Tasks[at].Turns = append(result.Tasks[at].Turns, turn)
	}
	return result, nil
}

// conversationSnippet keeps the match visible even on a long output line.
// Its input was redacted before a cut could hide half a legacy credential.
func conversationSnippet(text string, words []string) string {
	runes := []rune(text)
	if len(runes) <= searchLineCut {
		return text
	}
	lower := strings.ToLower(text)
	at := len(lower)
	for _, word := range words {
		if i := strings.Index(lower, strings.ToLower(word)); i >= 0 && i < at {
			at = i
		}
	}
	// The lowercase spelling can have a different byte width; count its runes.
	start := 0
	if at < len(lower) {
		start = max(0, utf8.RuneCountInString(lower[:at])-60)
	}
	end := min(len(runes), start+searchLineCut)
	snippet := string(runes[start:end])
	if start > 0 {
		snippet = "…" + snippet
	}
	if end < len(runes) {
		snippet += "…"
	}
	return snippet
}
