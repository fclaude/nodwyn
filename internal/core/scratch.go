package core

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/fclaude/nodwyn/internal/config"
)

// The scratch directory is where a session's throwaway files go: scripts
// that reproduce a bug, notes, downloaded output. It is outside the
// repository, so none of it reaches the repository's diff; the file
// primitives may read and write it without prompts, shell-exec gets it as
// TMPDIR, and it is removed when the session closes. A resumed session gets
// a new, empty one.
//
// Each directory is made by os.MkdirTemp (unpredictable name, mode 0700)
// under the system's temporary directory, so no other user can plant or
// share it, and outside nodwyn's configuration and data directories, which
// the guard protects: a program run with that TMPDIR (a test suite that
// builds temporary repositories) is not refused its own files. Until
// 2026-10-16 it lived in the data directory (ADR 28), where on the
// benchmark 5 to 17 calls of a run went on working around it. Directories a
// crashed session left behind are swept once they are a week old.

// scratchMaxAge is how long a scratch directory may outlive its session.
const scratchMaxAge = 7 * 24 * time.Hour

// scratchRoot holds the sessions' scratch directories: the system's
// temporary directory.
func scratchRoot() string {
	return config.CanonicalPath(os.TempDir())
}

// legacyScratchRoot is where sessions before 2026-10-16 kept theirs:
// dataDir/scratch. Nothing is made there now; the sweep still clears what
// those sessions left, so a crashed one does not leave its directory for
// good.
func legacyScratchRoot(dataDir string) string {
	if dataDir == "" {
		return ""
	}
	return config.CanonicalPath(filepath.Join(dataDir, "scratch"))
}

// makeScratch creates a scratch directory for the session and returns its
// path with symbolic links resolved (macOS's temporary directory is behind
// one), which is the path the tools are given.
func makeScratch(dataDir, id string) (string, error) {
	if id == "" || strings.ContainsAny(id, `/\`) || strings.HasPrefix(id, ".") {
		return "", fmt.Errorf("core: scratch: bad session id %q", id)
	}
	root := scratchRoot()
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", fmt.Errorf("core: scratch: %w", err)
	}
	sweepScratch(root)
	if legacy := legacyScratchRoot(dataDir); legacy != "" {
		sweepScratch(legacy)
	}
	dir, err := os.MkdirTemp(root, "nodwyn-scratch-"+id+"-")
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
	if dir != "" && strings.HasPrefix(filepath.Base(dir), "nodwyn-scratch-") {
		_ = os.RemoveAll(dir)
	}
}

// sweepScratch removes scratch directories older than scratchMaxAge: those
// of sessions that ended without closing. A session touches its directory
// at every turn (touchScratch), so an open one is never this old. The
// system's temporary directory is shared, so only a directory of this
// user's own, not a link to one, is removed.
func sweepScratch(root string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "nodwyn-scratch-") {
			continue
		}
		if info, err := e.Info(); err == nil && ownedByUs(info) && time.Since(info.ModTime()) > scratchMaxAge {
			_ = os.RemoveAll(filepath.Join(root, e.Name()))
		}
	}
}

// ownedByUs reports whether info, from a directory listing (so a link is
// described, not followed), is of a directory this user made.
func ownedByUs(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && info.Mode().IsDir() && int(st.Uid) == os.Getuid()
}

// notesFile is where a repository's notes kept across sessions live:
// dataDir/projects/<hash of the repository's path>/notes.md, outside the
// repository so they never reach its diff; "" without a data directory.
func notesFile(dataDir, repo string) string {
	if dir := projectDir(dataDir, repo); dir != "" {
		return filepath.Join(dir, "notes.md")
	}
	return ""
}

// projectDir is where nodwyn keeps what is a repository's across sessions,
// outside it: dataDir/projects/<hash of the repository's path>, its notes
// and its index (ADR 50); "" without a data directory.
func projectDir(dataDir, repo string) string {
	if dataDir == "" || repo == "" {
		return ""
	}
	abs, err := filepath.Abs(repo)
	if err != nil {
		abs = repo
	}
	sum := sha256.Sum256([]byte(config.CanonicalPath(abs)))
	return filepath.Join(dataDir, "projects", hex.EncodeToString(sum[:8]))
}
