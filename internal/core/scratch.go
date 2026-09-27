package core

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The scratch directory is where a session's throwaway files go: scripts
// that reproduce a bug, notes, downloaded output. It is outside the
// repository, so none of it reaches the repository's diff; the file
// primitives may read and write it without prompts, shell-exec gets it as
// TMPDIR, and it is removed when the session closes. A resumed session gets
// the same directory back.

// scratchRoot holds every session's scratch directory.
func scratchRoot() string { return filepath.Join(os.TempDir(), "nibb-scratch") }

// makeScratch creates the session's scratch directory, private to the
// user, and returns its path with symbolic links resolved (macOS's
// temporary directory is behind one), which is the path the model is told.
func makeScratch(id string) (string, error) {
	if id == "" || strings.ContainsAny(id, `/\`) || strings.HasPrefix(id, ".") {
		return "", fmt.Errorf("core: scratch: bad session id %q", id)
	}
	dir := filepath.Join(scratchRoot(), id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("core: scratch: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", fmt.Errorf("core: scratch: %w", err)
	}
	return resolved, nil
}

// removeScratch deletes a scratch directory made by makeScratch, and
// nothing else.
func removeScratch(dir string) {
	if dir == "" {
		return
	}
	root, err := filepath.EvalSymlinks(scratchRoot())
	if err != nil || filepath.Dir(dir) != root {
		return
	}
	_ = os.RemoveAll(dir)
}
