package core

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The scratch directory is where a session's throwaway files go: scripts
// that reproduce a bug, notes, downloaded output. It is outside the
// repository, so none of it reaches the repository's diff; the file
// primitives may read and write it without prompts, shell-exec gets it as
// TMPDIR, and it is removed when the session closes. A resumed session gets
// a new, empty one.
//
// Each directory is made by os.MkdirTemp (unpredictable name, mode 0700)
// under nibb's own data directory, so no other user can plant or share it;
// only a session without a data directory (tests, embedders) falls back to
// the system's temporary directory. Directories a crashed session left
// behind are swept once they are a week old.

// scratchMaxAge is how long a scratch directory may outlive its session.
const scratchMaxAge = 7 * 24 * time.Hour

// scratchRoot holds the sessions' scratch directories: dataDir/scratch, or
// the system temporary directory when there is no data directory.
func scratchRoot(dataDir string) string {
	if dataDir == "" {
		return os.TempDir()
	}
	return filepath.Join(dataDir, "scratch")
}

// makeScratch creates a scratch directory for the session and returns its
// path with symbolic links resolved (macOS's temporary directory is behind
// one), which is the path the tools are given.
func makeScratch(dataDir, id string) (string, error) {
	if id == "" || strings.ContainsAny(id, `/\`) || strings.HasPrefix(id, ".") {
		return "", fmt.Errorf("core: scratch: bad session id %q", id)
	}
	root := scratchRoot(dataDir)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", fmt.Errorf("core: scratch: %w", err)
	}
	sweepScratch(root)
	dir, err := os.MkdirTemp(root, "nibb-scratch-"+id+"-")
	if err != nil {
		return "", fmt.Errorf("core: scratch: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", fmt.Errorf("core: scratch: %w", err)
	}
	return resolved, nil
}

// expandScratch rewrites a leading $TMPDIR (or ${TMPDIR}) in a tool's path
// or command argument to the scratch directory. The prompt names the
// directory as $TMPDIR, which shell-exec's environment defines, so the
// same name must work in the file tools and in a command's arguments, which
// run without a shell; without it, "$TMPDIR/repro.py" would be a directory
// of that name inside the repository.
func expandScratch(dir string) func(string) string {
	if dir == "" {
		return nil
	}
	return func(s string) string {
		for _, v := range []string{"$TMPDIR", "${TMPDIR}"} {
			if s == v {
				return dir
			}
			if strings.HasPrefix(s, v+"/") {
				return dir + s[len(v):]
			}
		}
		return s
	}
}

// touchScratch marks the scratch directory as in use, so the sweep, which
// goes by its modification time, never removes a live session's directory,
// however long the session stays open.
func touchScratch(dir string) {
	if dir != "" {
		now := time.Now()
		_ = os.Chtimes(dir, now, now)
	}
}

// removeScratch deletes a scratch directory made by makeScratch, and
// nothing else.
func removeScratch(dir string) {
	if dir != "" && strings.HasPrefix(filepath.Base(dir), "nibb-scratch-") {
		_ = os.RemoveAll(dir)
	}
}

// sweepScratch removes scratch directories older than scratchMaxAge: those
// of sessions that ended without closing. A session touches its directory
// at every turn (touchScratch), so an open one is never this old.
func sweepScratch(root string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "nibb-scratch-") {
			continue
		}
		if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > scratchMaxAge {
			_ = os.RemoveAll(filepath.Join(root, e.Name()))
		}
	}
}
