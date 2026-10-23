package core

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/fclaude/nodwyn/internal/log"
)

// titleActor is who a generated title is attributed to in the log: the
// model, never a person, so a reader tells a name someone gave the task
// from one it was given.
const titleActor = "model"

// maxTitle bounds a generated title as session.rename bounds a person's
// name (rpc maxName): a line in a list.
const maxTitle = 120

// untitledAndUnasked says whether a session whose log is events has had no
// message yet and no name: only then is its first message the one a title
// is asked for. A session a person or a workflow named before it was
// written to keeps that name, and a session resumed with messages in it is
// not asked again.
func untitledAndUnasked(events []log.Event) bool {
	for _, ev := range events {
		if ev.Kind == log.KindUserMsg || ev.Kind == log.KindSessionTitle {
			return false
		}
	}
	return true
}

// titleFrom asks the model for a short title for request, the session's
// first message, and records it as the session's name, attributed to the
// model (ADR 39, amended 2026-10-23). It goes through the session's own
// Record, so it crosses the log's redaction boundary as a person's name
// does. The question is asked in a context of its own, folded away before
// the turn starts, so the conversation never sees it. A failed request or
// an empty reply records nothing: the task is read by its first message, as
// before, and the turn goes on.
func (s *Session) titleFrom(ctx context.Context, request string) {
	reply, err := s.loop.Title(ctx, request)
	if err != nil {
		return
	}
	if title := cleanTitle(reply); title != "" {
		_, _ = s.Record(log.KindSessionTitle, log.SessionTitle{Actor: titleActor, Title: title})
	}
}

// cleanTitle is a model's reply made a title: its whitespace collapsed as
// session.rename collapses a person's name, without the quotes, backticks
// or emphasis a model puts around a title or the full stop it ends one
// with, and cut at a word to at most maxTitle characters.
func cleanTitle(reply string) string {
	title := strings.Join(strings.Fields(reply), " ")
	title = strings.Trim(title, "\"'`*\u201c\u201d\u2018\u2019 ")
	title = strings.TrimSpace(strings.TrimSuffix(title, "."))
	if utf8.RuneCountInString(title) > maxTitle {
		cut := string([]rune(title)[:maxTitle])
		if space := strings.LastIndex(cut, " "); space > maxTitle/2 {
			cut = cut[:space]
		}
		title = strings.TrimRight(cut, " ,;:")
	}
	return title
}
