package core

import (
	"errors"
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

// nameTask answers the name-task tool: the model's name for the session,
// from its first request (ADR 39, amended 2026-10-23). It goes through the
// session's own Record, so it crosses the log's redaction boundary as a
// person's name does. A session that has any session.title in its log, a
// person's, a workflow's, an earlier call's or one a person took back with
// an empty name, is not named again: a name a person gave or took back is
// theirs, and a model that calls the tool again changes nothing. A model
// that never calls it leaves the task read by its first message, as before.
func (s *Session) nameTask(reply string) (string, error) {
	title := cleanTitle(reply)
	if title == "" {
		return "", errors.New("name-task takes three to seven words naming the task")
	}
	for _, ev := range s.EventsSoFar() {
		if ev.Kind == log.KindSessionTitle {
			return "The task has a name already; nothing changed.", nil
		}
	}
	if _, err := s.Record(log.KindSessionTitle, log.SessionTitle{Actor: titleActor, Title: title}); err != nil {
		return "", err
	}
	return "Named.", nil
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
