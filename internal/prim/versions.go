package prim

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/fclaude/nodwyn/internal/config"
)

// Versions keeps a file's previous content before nodwyn replaces it, where
// git is not the undo (ADR 22 §2): content-addressed objects under
// <root>/.nodwyn/versions/objects and an append-only index. It is capped per
// file and in total; the oldest versions go first.
type Versions struct {
	Dir string // <root>/.nodwyn/versions; a worktree session's is its repository's
	// Skip reports paths another undo already covers (a git work tree).
	Skip func(abs string) bool
	// Record is told of every kept version, for the session log.
	Record func(Version)
}

// Version is one kept previous content of a file.
type Version struct {
	Path  string    `json:"path"` // absolute
	Hash  string    `json:"hash"` // sha256 of the content
	Bytes int       `json:"bytes"`
	Time  time.Time `json:"time"`
}

const (
	maxVersionFile    = 16 << 20  // a larger file is not overwritten, as no version of it can be kept
	maxVersionsPer    = 20        // per path
	maxVersionsTotal  = 256 << 20 // all objects together
	versionsIndexName = "index.jsonl"
)

var hashName = regexp.MustCompile(`^[0-9a-f]{64}$`)

// VersionsDir is where root's versions live.
func VersionsDir(root string) string { return filepath.Join(root, ".nodwyn", "versions") }

// Keep saves abs's current content, if it has any and it is not covered by
// another undo. A link is followed, as the write that comes next follows it:
// what is kept is the target's content, under the target's path. A failure to
// keep a version fails the write, and so does a file too large to keep: a
// workspace that promises an undo must not lose one silently.
func (v *Versions) Keep(abs string) error {
	if v == nil || (v.Skip != nil && v.Skip(abs)) {
		return nil
	}
	info, err := os.Stat(abs)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return nil
	}
	if info.Size() > maxVersionFile {
		return fmt.Errorf("%s is %s, more than the %s a kept version holds", abs, byteCount(int(info.Size())), byteCount(maxVersionFile))
	}
	content, err := os.ReadFile(abs)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(content)
	version := Version{Path: abs, Hash: hex.EncodeToString(sum[:]), Bytes: len(content), Time: time.Now().UTC()}
	objects := filepath.Join(v.Dir, "objects")
	if err := os.MkdirAll(objects, 0o700); err != nil {
		return err
	}
	// The sessions of one repository share this store, and a pruning one
	// rewrites the index: a version another appended meanwhile would be lost,
	// its object orphaned, and an object it had just found present removed.
	unlock, err := lockStore(v.Dir)
	if err != nil {
		return err
	}
	defer unlock()
	// Keep nodwyn's copies out of a repository's commits.
	if ignore := filepath.Join(v.Dir, ".gitignore"); !exists(ignore) {
		_ = os.WriteFile(ignore, []byte("# nodwyn's kept versions of files\n*\n"), 0o600)
	}
	object := filepath.Join(objects, version.Hash)
	if _, err := os.Stat(object); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(object, content, 0o600); err != nil {
			return err
		}
	}
	line, _ := json.Marshal(version)
	f, err := os.OpenFile(filepath.Join(v.Dir, versionsIndexName), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(append(line, '\n'))
	if err := errors.Join(werr, f.Close()); err != nil {
		return err
	}
	if v.Record != nil {
		v.Record(version)
	}
	return v.prune()
}

// lockStore holds an advisory lock on the store at dir, against the other
// sessions and daemons that use it. The lock is on the file description, so
// it also excludes another Keep in this process.
func lockStore(dir string) (func(), error) {
	fd, err := syscall.Open(filepath.Join(dir, "lock"), syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("versions: lock: %w", err)
	}
	for {
		if err = syscall.Flock(fd, syscall.LOCK_EX); !errors.Is(err, syscall.EINTR) {
			break
		}
	}
	if err != nil {
		syscall.Close(fd)
		return nil, fmt.Errorf("versions: lock: %w", err)
	}
	return func() { _ = syscall.Flock(fd, syscall.LOCK_UN); _ = syscall.Close(fd) }, nil
}

// prune keeps the newest maxVersionsPer versions of each path and at most
// maxVersionsTotal bytes of objects, removing objects nothing refers to. The
// store is locked.
func (v *Versions) prune() error {
	all, err := readIndex(v.Dir)
	if err != nil {
		return err
	}
	perPath := map[string]int{}
	kept := make([]Version, 0, len(all))
	var total int
	sizes := map[string]int{}
	for i := len(all) - 1; i >= 0; i-- { // newest first
		x := all[i]
		if perPath[x.Path] >= maxVersionsPer {
			continue
		}
		if _, counted := sizes[x.Hash]; !counted && total+x.Bytes > maxVersionsTotal {
			continue
		}
		perPath[x.Path]++
		if _, counted := sizes[x.Hash]; !counted {
			sizes[x.Hash] = x.Bytes
			total += x.Bytes
		}
		kept = append(kept, x)
	}
	if len(kept) == len(all) {
		return nil
	}
	sort.Slice(kept, func(i, j int) bool { return kept[i].Time.Before(kept[j].Time) })
	var buf bytes.Buffer
	for _, x := range kept {
		line, _ := json.Marshal(x)
		buf.Write(append(line, '\n'))
	}
	index := filepath.Join(v.Dir, versionsIndexName)
	tmp := index + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, index); err != nil {
		return err
	}
	for _, x := range all {
		if _, ok := sizes[x.Hash]; !ok {
			_ = os.Remove(filepath.Join(v.Dir, "objects", x.Hash))
		}
	}
	return nil
}

func readIndex(dir string) ([]Version, error) {
	f, err := os.Open(filepath.Join(dir, versionsIndexName))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Version
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		var x Version
		if json.Unmarshal(scanner.Bytes(), &x) == nil && hashName.MatchString(x.Hash) {
			x.Path = config.CanonicalPath(x.Path)
			out = append(out, x)
		}
	}
	return out, scanner.Err()
}

// ListVersions returns the kept versions of abs in dir, newest first.
func ListVersions(dir, abs string) ([]Version, error) {
	all, err := readIndex(dir)
	if err != nil {
		return nil, err
	}
	var out []Version
	for i := len(all) - 1; i >= 0; i-- {
		if all[i].Path == config.CanonicalPath(abs) {
			out = append(out, all[i])
		}
	}
	return out, nil
}

// ReadVersion returns a kept object's content.
func ReadVersion(dir, hash string) ([]byte, error) {
	if !hashName.MatchString(hash) {
		return nil, fmt.Errorf("versions: %q is not a version", hash)
	}
	// A repository writer controls the store; an object is content, not a link's authority.
	pin, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer pin.Close()
	name := filepath.Join("objects", hash)
	info, err := pin.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("versions: object must be a regular file")
	}
	file, err := pin.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, maxVersionFile+1))
	if err != nil {
		return nil, err
	}
	if len(content) > maxVersionFile {
		return nil, fmt.Errorf("versions: object is too large")
	}
	sum := sha256.Sum256(content)
	if hex.EncodeToString(sum[:]) != hash {
		return nil, fmt.Errorf("versions: object %s is damaged", hash[:12])
	}
	return content, nil
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// GitHoldsPath is root's Versions.Skip: it reports whether git is the undo
// for a path: it is inside the git work tree at root, tracked, and neither
// the index nor the work tree changes it, so git gives its content back.
// Since writes go ahead without asking (ADR 35), an uncommitted change, an
// untracked or ignored file, and a file git cannot be asked about keep a
// version: git could not restore them.
func GitHoldsPath(root string) func(string) bool {
	if !isGitWorkTree(root) {
		return func(string) bool { return false }
	}
	o := DefaultOptions(root)
	return func(abs string) bool {
		if abs != root && !strings.HasPrefix(abs, root+string(filepath.Separator)) {
			return false
		}
		rel, err := filepath.Rel(root, abs)
		if err != nil {
			return false
		}
		// Status names an existing file unless it is tracked and unchanged:
		// ?? untracked, !! ignored, and the codes of a change.
		out, err := runGit(context.Background(), o, "status", "--porcelain", "--ignored", "--untracked-files=all", "--", ":(literal)"+rel)
		return err == nil && strings.TrimSpace(out) == ""
	}
}
