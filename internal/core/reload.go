package core

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/fclaude/nodwyn/internal/scheme"
)

// registerReload adds (reload): run .nodwyn/init.scm again in this session, so
// a changed strategy, compaction setting or agent-tool handler applies from
// the next turn. What a session is fixed with does not change (ADR 9): the
// tool list, the guard's checks and the prompts. reload returns what
// changed there as notes; those apply from the next session or a resume.
func (s *Session) registerReload() error {
	return s.interp.Register(&scheme.Prim{
		Name: "reload",
		Meta: scheme.Meta{
			Doc:    "Run .nodwyn/init.scm again in this session, with the caller's origin. The strategy, compaction settings and agent-tool handlers apply from the next turn; a tool keeps its schema and takes a changed body. Returns a list of notes on what is fixed for the session and applies from the next one: new or changed tools, guard checks, prompts.",
			Hidden: true,
			Origin: "builtin",
		},
		Handler: func(c *scheme.Call) (scheme.Value, error) {
			if c.Origin == scheme.OriginModel {
				return scheme.Void, errors.New("not callable from a model-originated evaluation")
			}
			init := s.opts.InitFile
			if init == "" {
				init = filepath.Join(s.opts.Repo, ".nodwyn", "init.scm")
			}
			if !fileExists(init) {
				return scheme.Void, fmt.Errorf("no init file at %s", init)
			}
			s.interp.BeginReload()
			err := s.interp.LoadFile(scheme.WithOrigin(c.Ctx, c.Origin), init)
			notes := s.interp.EndReload()
			if err != nil {
				return scheme.Void, fmt.Errorf("reload: %w", err)
			}
			changed, err := s.loop.PromptChanges(s.opts.Repo)
			if err != nil {
				return scheme.Void, fmt.Errorf("reload: %w", err)
			}
			for _, name := range changed {
				notes = append(notes, fmt.Sprintf("%s changed: prompts are fixed for this session, so it applies from the next one", name))
			}
			out := make([]scheme.Value, len(notes))
			for i, n := range notes {
				out[i] = scheme.Str(n)
			}
			return scheme.List(out...), nil
		},
	})
}
