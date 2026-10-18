package rpc

import (
	"context"
	"path/filepath"
	"time"

	"github.com/fclaude/nodwyn/internal/config"
)

// trustShown is the most of either project file the console is shown, which
// is what a person reviews: a pair too large to read whole is not offered
// for trust here, since trust is a review of exactly what will run.
const trustShown = 256 << 10

type trustParams struct {
	Repo string `json:"repo"`
}

type trustSetParams struct {
	Repo string `json:"repo"`
	// Revision is the one the person reviewed (trust.read's); empty revokes.
	Revision string `json:"revision"`
}

// trustState is a project's startup files, as the person reviews them.
type trustState struct {
	Repo string `json:"repo"`
	// Present says the project has an init.scm or an allow.toml.
	Present bool `json:"present"`
	// Trusted says the user-owned record holds this revision.
	Trusted  bool   `json:"trusted"`
	Revision string `json:"revision,omitempty"`
	// Init and Allow are the files' text, nil for a file that is absent, and
	// both nil with Reviewable false when either is too large to show whole.
	Init       *string `json:"init"`
	Allow      *string `json:"allow"`
	Reviewable bool    `json:"reviewable"`
}

// trustRoot is a folder the daemon works in, canonical and not refused as a
// task's root: what trust.read and trust.set may name.
func (s *server) trustRoot(repo string) (string, *rpcError) {
	root := config.CanonicalPath(repo)
	if _, known := s.versionStore(root); !filepath.IsAbs(repo) || !known {
		return "", failure(-32602, "repo must be a folder nodwyn works in")
	}
	if reason := s.rootRefusal(root); reason != "" {
		return "", failure(-32602, reason)
	}
	return root, nil
}

func (s *server) trustState(root string) (trustState, *rpcError) {
	files, err := config.ReadProjectFiles(root)
	if err != nil {
		return trustState{}, failure(-32000, err.Error())
	}
	state := trustState{Repo: root, Present: files.Present(), Reviewable: true}
	if !state.Present {
		return state, nil
	}
	state.Revision = files.Revision()
	if trusted, err := config.ProjectTrusted(s.options.Paths, root, files); err != nil {
		return trustState{}, failure(-32000, err.Error())
	} else {
		state.Trusted = trusted
	}
	if len(files.Init) > trustShown || len(files.Allow) > trustShown {
		state.Reviewable = false
		return state, nil
	}
	text := func(b []byte) *string {
		if b == nil {
			return nil
		}
		t := string(b)
		return &t
	}
	state.Init, state.Allow = text(files.Init), text(files.Allow)
	return state, nil
}

// trustMethod answers trust.read and trust.set: the review `nodwyn trust`
// prints, and its accept and revoke, for the console of a person signed in
// with a passkey (ADR 45, amended 2026-10-17). The files' author is whoever
// can commit to the repository, so a grant names the revision the person
// read, and TrustProject refuses it unless the files are still exactly that.
func (s *server) trustMethod(req request, client *peer) (any, *rpcError) {
	if req.Method == "trust.read" {
		var p trustParams
		if err := params(req.Params, &p, "repo"); err != nil {
			return nil, err
		}
		root, rerr := s.trustRoot(p.Repo)
		if rerr != nil {
			return nil, rerr
		}
		return s.trustState(root)
	}
	if why := client.refuseDevice("Project trust is granted at a terminal, or from a device signed in with a passkey, not from a paired device"); why != nil {
		return nil, why
	}
	var p trustSetParams
	if err := params(req.Params, &p, "repo", "revision"); err != nil {
		return nil, err
	}
	root, rerr := s.trustRoot(p.Repo)
	if rerr != nil {
		return nil, rerr
	}
	// Reading the state first says "nothing to trust" for a project with no
	// startup files, and a revision for one that has them.
	if state, rerr := s.trustState(root); rerr != nil {
		return nil, rerr
	} else if p.Revision != "" && !state.Present {
		return nil, failure(-32000, "This project has no init.scm or allow.toml to trust")
	}
	ctx, cancel := context.WithTimeout(s.base(), 10*time.Second)
	defer cancel()
	if err := config.TrustProject(ctx, s.options.Paths, root, p.Revision); err != nil {
		return nil, failure(-32000, err.Error())
	}
	return s.trustState(root)
}
