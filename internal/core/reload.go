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
			Doc:    "Run the currently trusted .nodwyn/init.scm in this session, with the caller's origin. Changed project files require review with nodwyn trust first. The strategy, compaction settings and agent-tool handlers apply from the next turn; a tool keeps its schema and takes a changed body. Returns a list of notes on what is fixed for the session and applies from the next one: new or changed tools, guard checks, prompts.",
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
			var source []byte
			if s.opts.InitFile == "" {
				files, err := s.trustedProject()
				if err != nil {
					return scheme.Void, err
				}
				if files.Init == nil {
					return scheme.Void, &scheme.Error{Message: "reload: review changed project files with nodwyn trust first", Irritants: []scheme.Value{scheme.Sym("project-trust-required")}}
				}
				source = files.Init
			}
			// The file runs again over what it found the first time, not over
			// itself: a wrapper's (define base-x x) would otherwise capture
			// the wrapper it made before, which then calls itself. A reload
			// that fails leaves every global as it was.
			global := s.interp.Global()
			before := global.Snapshot()
			global.Restore(s.initFresh(before))
			fresh := global.Snapshot()
			s.interp.BeginReload()
			var err error
			if s.opts.InitFile != "" {
				err = s.interp.LoadFile(scheme.WithOrigin(c.Ctx, c.Origin), init)
			} else {
				_, err = s.interp.EvalString(scheme.WithOrigin(c.Ctx, c.Origin), string(source))
			}
			notes := s.interp.EndReload()
			if err != nil {
				global.Restore(before)
				return scheme.Void, fmt.Errorf("reload: %w", err)
			}
			changed, err := s.loop.PromptChanges(s.opts.Repo)
			if err != nil {
				global.Restore(before)
				return scheme.Void, fmt.Errorf("reload: %w", err)
			}
			s.noteInitChanges(fresh, global.Snapshot())
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

// initDefault is how a global was bound before init.scm first changed it.
type initDefault struct {
	value scheme.Value
	bound bool
}

// noteInitChanges records, for each global that init.scm changed between
// two snapshots, its binding before the first change. A primitive is not
// recorded: the name register-tool binds belongs to the session's tool
// table, which a reload keeps and only gives a changed body.
func (s *Session) noteInitChanges(before, after map[string]scheme.Value) {
	if s.initDefaults == nil {
		s.initDefaults = map[string]initDefault{}
	}
	for name, v := range after {
		old, had := before[name]
		if (had && scheme.Eqv(old, v)) || v.Kind == scheme.KPrim {
			continue
		}
		if _, seen := s.initDefaults[name]; !seen {
			s.initDefaults[name] = initDefault{old, had}
		}
	}
}

// initFresh is current with every global init.scm changed put back as it
// was before init.scm ran. Globals the turn changes, such as a strategy's
// counters, are left as they are.
func (s *Session) initFresh(current map[string]scheme.Value) map[string]scheme.Value {
	fresh := make(map[string]scheme.Value, len(current))
	for name, v := range current {
		fresh[name] = v
	}
	for name, d := range s.initDefaults {
		if d.bound {
			fresh[name] = d.value
		} else {
			delete(fresh, name)
		}
	}
	return fresh
}
