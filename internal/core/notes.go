package core

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/event"
	"github.com/fclaude/nodwyn/internal/prim"
	"github.com/fclaude/nodwyn/internal/scheme"
)

// ProjectNotes reads a repository's notes kept across sessions, the text
// (project-notes) returns in a session there: "" when there are none. It
// needs no session, so a front-end can show the notes of a saved task.
func ProjectNotes(dataDir, repo string) (string, error) {
	path := notesFile(dataDir, repo)
	if path == "" {
		return "", nil
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	return string(b), err
}

// SaveNotes replaces this repository's notes on a person's behalf. It applies
// the registered project-notes-save primitive with user origin, so the save
// crosses the guard like any other (text that looks like a credential still
// needs a person) and is recorded as that actor's evaluation. The outcome
// arrives as a policy.result event named "notes" carrying the returned ID.
func (s *Session) SaveNotes(text, actor string) (string, error) {
	if actor == "" {
		return "", fmt.Errorf("notes: actor is required")
	}
	id := "i" + rand.Text()
	err := s.startTurn(func(ctx context.Context) error {
		result := event.Policy{ID: id, Name: "notes"}
		// The registered host primitive itself, never whatever the Scheme
		// environment binds to the name.
		p, ok := s.interp.Lookup("project-notes-save")
		var err error
		if !ok {
			err = fmt.Errorf("project notes are unavailable in this session")
		} else {
			args := []scheme.Value{scheme.Str(text)}
			source := scheme.Write(scheme.List(scheme.Sym("project-notes-save"), args[0]))
			_, err = s.recordEvaluation(source, actor, func() (scheme.Value, error) {
				return s.interp.Apply(scheme.WithOrigin(ctx, scheme.OriginUser), scheme.Value{Kind: scheme.KPrim, Prim: p}, args)
			})
		}
		if err != nil {
			result.Err = err.Error()
		} else {
			result.Saved = true
		}
		s.Emit(event.Event{Kind: event.PolicyResult, Payload: result})
		return err
	})
	if err != nil {
		return "", err
	}
	return id, nil
}

// Workspace returns the workspace settings the session loaded.
func (s *Session) Workspace() config.Workspace { return s.workspace }

// Restore writes a kept version of a file back on a person's behalf. It
// applies the registered fs-write with user origin, so the guard shows the
// change and asks like any other write, and the current content is itself
// kept first: a restore can be undone the same way. The outcome arrives as a
// policy.result event named "restore" carrying the returned ID.
func (s *Session) Restore(path, hash, actor string) (string, error) {
	if actor == "" {
		return "", fmt.Errorf("restore: actor is required")
	}
	abs := path
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(s.opts.Repo, abs)
	}
	abs = filepath.Clean(abs)
	content, err := prim.ReadVersion(prim.VersionsDir(s.opts.Repo), hash)
	if err != nil {
		return "", err
	}
	known, err := prim.ListVersions(prim.VersionsDir(s.opts.Repo), abs)
	if err != nil {
		return "", err
	}
	if !slices.ContainsFunc(known, func(v prim.Version) bool { return v.Hash == hash }) {
		return "", fmt.Errorf("restore: %s has no kept version %s", path, hash[:min(12, len(hash))])
	}
	target := abs
	if rel, err := filepath.Rel(s.opts.Repo, abs); err == nil && !strings.HasPrefix(rel, "..") {
		target = rel
	}
	id := "i" + rand.Text()
	err = s.startTurn(func(ctx context.Context) error {
		result := event.Policy{ID: id, Name: "restore"}
		p, ok := s.interp.Lookup("fs-write")
		var err error
		if !ok {
			err = fmt.Errorf("writing files is unavailable in this session")
		} else {
			args := []scheme.Value{scheme.Str(target), scheme.Str(string(content))}
			source := scheme.Write(scheme.List(scheme.Sym("fs-write"), args[0], scheme.Str(fmt.Sprintf("<version %s>", hash[:12]))))
			_, err = s.recordEvaluation(source, actor, func() (scheme.Value, error) {
				return s.interp.Apply(scheme.WithOrigin(ctx, scheme.OriginUser), scheme.Value{Kind: scheme.KPrim, Prim: p}, args)
			})
		}
		if err != nil {
			result.Err = err.Error()
		} else {
			result.Saved = true
		}
		s.Emit(event.Event{Kind: event.PolicyResult, Payload: result})
		return err
	})
	if err != nil {
		return "", err
	}
	return id, nil
}
