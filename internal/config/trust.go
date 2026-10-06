package config

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/fclaude/nodwyn/internal/policyfile"
)

// ProjectFiles is a snapshot of the repository files that request authority.
// Their author is whoever can commit to the repository, not necessarily its user.
type ProjectFiles struct {
	Init, Allow []byte
}

func (f ProjectFiles) Present() bool { return f.Init != nil || f.Allow != nil }

// Revision includes presence as well as contents, so adding a formerly absent
// file requires another review. The snapshot itself is what the session loads.
func (f ProjectFiles) Revision() string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(policyfile.Revision(f.Init)+"\n"+policyfile.Revision(f.Allow))))
}

func ReadProjectFiles(repo string) (ProjectFiles, error) {
	dir := filepath.Join(repo, FolderDir)
	if info, err := os.Lstat(dir); err == nil && !info.IsDir() {
		return ProjectFiles{}, errors.New("project trust: .nodwyn must be a directory, not a link")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return ProjectFiles{}, err
	}
	init, err := policyfile.Read(filepath.Join(dir, "init.scm"), 4<<20)
	if err != nil {
		return ProjectFiles{}, err
	}
	allow, err := policyfile.Read(filepath.Join(dir, "allow.toml"), 1<<20)
	return ProjectFiles{Init: init, Allow: allow}, err
}

func trustFile(p Paths) string { return filepath.Join(p.ConfigDir, "trusted-projects.json") }

func parseTrust(raw []byte) (map[string]string, error) {
	entries := map[string]string{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &entries); err != nil {
			return nil, fmt.Errorf("project trust: %w", err)
		}
	}
	if entries == nil {
		entries = map[string]string{}
	}
	return entries, nil
}

// ProjectTrusted consults only the user-owned configuration directory. Neither
// an allow file nor a session log is authority to grant repository startup code.
func ProjectTrusted(p Paths, project string, files ProjectFiles) (bool, error) {
	if p.ConfigDir == "" || !files.Present() {
		return false, nil
	}
	raw, err := policyfile.Read(trustFile(p), 4<<20)
	if err != nil {
		return false, err
	}
	entries, err := parseTrust(raw)
	return entries[CanonicalPath(project)] == files.Revision(), err
}

// TrustProject records an explicitly reviewed revision, or revokes it when
// revision is empty. It is a host operation, never a Scheme primitive. A worktree
// may use its main project's trust only when both files are exactly the same.
func TrustProject(ctx context.Context, p Paths, project, revision string) error {
	if p.ConfigDir == "" {
		return errors.New("project trust: a user configuration directory is required")
	}
	project = CanonicalPath(project)
	if revision != "" {
		files, err := ReadProjectFiles(project)
		if err != nil {
			return err
		}
		if !files.Present() || revision != files.Revision() {
			return errors.New("project trust: files changed or are absent; review them again")
		}
		if _, err := ParseRepoAllow(files.Allow); err != nil {
			return err
		}
	}
	return policyfile.Update(ctx, trustFile(p), func(raw []byte) ([]byte, error) {
		entries, err := parseTrust(raw)
		if err != nil {
			return nil, err
		}
		if revision == "" {
			delete(entries, project)
		} else {
			entries[project] = revision
		}
		return json.MarshalIndent(entries, "", "  ")
	})
}
