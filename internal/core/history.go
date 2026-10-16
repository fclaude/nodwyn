package core

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/fclaude/nodwyn/internal/redact"
	"github.com/fclaude/nodwyn/internal/scheme"
)

// registerHistory adds session-list and session-events: read-only views of
// this repository's sessions, the history a strategy such as the optimizer
// learns from (docs/scheme.md §9.4). Other repositories' sessions are not
// visible. Payloads are redacted as they are read, as ReadLog does, and the
// values carry provenance (external:log), like a tool's output.
func (s *Session) registerHistory() error {
	store := Store{Dir: s.opts.Paths.SessionsDir}
	for _, p := range []*scheme.Prim{
		{
			Name: "session-list",
			Meta: scheme.Meta{
				Doc:      "This repository's sessions, newest first: (session-list) for all, (session-list n) for the latest n. Each is an alist with keys id, first-message, last-event, current (this session), in-flight and truncated.",
				Variadic: true,
				Host:     true,
				Hidden:   true,
				Origin:   "builtin",
				// Logs hold file contents and command output.
				TaintSource: "external:log",
			},
			Handler: func(c *scheme.Call) (scheme.Value, error) {
				limit := -1
				switch len(c.Args) {
				case 0:
				case 1:
					n, err := c.Int(0)
					if err != nil {
						return scheme.Void, err
					}
					if n < 1 {
						return scheme.Void, errors.New("n must be positive")
					}
					limit = int(n)
				default:
					return scheme.Void, errors.New("expected at most one argument, how many sessions")
				}
				infos, err := Readable(store.List())
				if err != nil {
					return scheme.Void, err
				}
				slices.Reverse(infos) // IDs sort by start time
				var out []scheme.Value
				for _, info := range infos {
					// A repository's sessions include those in its
					// worktrees (ADR 34).
					if info.Project() != s.project() {
						continue
					}
					if limit >= 0 && len(out) == limit {
						break
					}
					out = append(out, scheme.List(
						scheme.Cons(scheme.Sym("id"), scheme.Str(info.ID)),
						scheme.Cons(scheme.Sym("first-message"), scheme.Str(info.FirstMessage)),
						scheme.Cons(scheme.Sym("last-event"), scheme.Str(info.LastEvent.UTC().Format(time.RFC3339))),
						scheme.Cons(scheme.Sym("current"), scheme.Bool(info.ID == s.id)),
						scheme.Cons(scheme.Sym("in-flight"), scheme.Bool(info.InFlight)),
						scheme.Cons(scheme.Sym("truncated"), scheme.Bool(info.Truncated)),
					))
				}
				return scheme.List(out...), nil
			},
		},
		{
			Name: "session-events",
			Meta: scheme.Meta{
				Doc:      "One of this repository's sessions as its log events: (session-events id) for all, (session-events id kinds) for the kinds listed, such as (\"user.msg\" \"llm.resp\" \"tool.result\"). Each event is an alist with keys seq, ts, kind and payload (the event's JSON, redacted, as json-parse gives it).",
				Params:   []scheme.Param{{Name: "id", Type: "string", Doc: "a session id from session-list"}},
				Variadic: true,
				Host:     true,
				Hidden:   true,
				Origin:   "builtin",
				// Logs hold file contents and command output.
				TaintSource: "external:log",
			},
			Handler: func(c *scheme.Call) (scheme.Value, error) {
				id, err := c.String(0)
				if err != nil {
					return scheme.Void, err
				}
				var kinds []string
				switch len(c.Args) {
				case 1:
				case 2:
					items, err := c.List(1)
					if err != nil {
						return scheme.Void, err
					}
					for _, item := range items {
						if item.Kind != scheme.KString {
							return scheme.Void, fmt.Errorf("kinds must be strings, got %s", scheme.Write(item))
						}
						kinds = append(kinds, item.S)
					}
				default:
					return scheme.Void, errors.New("expected a session id and, optionally, a list of kinds")
				}
				info, events, err := store.read(id)
				if err != nil {
					return scheme.Void, err
				}
				if info.Project() != s.project() {
					return scheme.Void, fmt.Errorf("session %s belongs to another repository", id)
				}
				var out []scheme.Value
				for _, ev := range events {
					if kinds != nil && !slices.Contains(kinds, string(ev.Kind)) {
						continue
					}
					payload := scheme.Value(scheme.Void)
					if len(ev.Payload) > 0 {
						raw, _, err := redact.JSON(ev.Payload, nil)
						if err != nil {
							return scheme.Void, err
						}
						if payload, err = scheme.FromJSON(raw); err != nil {
							return scheme.Void, err
						}
					}
					out = append(out, scheme.List(
						scheme.Cons(scheme.Sym("seq"), scheme.Int(int64(ev.Seq))),
						scheme.Cons(scheme.Sym("ts"), scheme.Str(ev.TS.UTC().Format(time.RFC3339))),
						scheme.Cons(scheme.Sym("kind"), scheme.Str(string(ev.Kind))),
						scheme.Cons(scheme.Sym("payload"), payload),
					))
				}
				return scheme.List(out...), nil
			},
		},
	} {
		if err := s.interp.Register(p); err != nil {
			return err
		}
	}
	return nil
}
