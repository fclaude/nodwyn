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
	"io/fs"
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
// workspace that promises an undo must not lose one silently. A link in the
// store is never followed (openStore).
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
	store, err := openStore(v.Dir)
	if err != nil {
		return err
	}
	defer store.Close()
	// The sessions of one repository share this store, and a pruning one
	// rewrites the index: a version another appended meanwhile would be lost,
	// its object orphaned, and an object it had just found present removed.
	unlock, err := lockStore(store)
	if err != nil {
		return err
	}
	defer unlock()
	// Keep nodwyn's copies out of a repository's commits. A file created
	// exclusively is never one a link names.
	if f, err := store.OpenFile(".gitignore", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600); err == nil {
		_, _ = f.WriteString("# nodwyn's kept versions of files\n*\n")
		_ = f.Close()
	}
	object := filepath.Join("objects", version.Hash)
	f, err := store.OpenFile(object, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	switch {
	case errors.Is(err, fs.ErrExist): // kept before, unless what is there is a link
		if err := regularIfThere(store, object); err != nil {
			return err
		}
	case err != nil:
		return err
	default:
		_, werr := f.Write(content)
		if err := errors.Join(werr, f.Close()); err != nil {
			return err
		}
	}
	line, _ := json.Marshal(version)
	if err := regularIfThere(store, versionsIndexName); err != nil {
		return err
	}
	f, err = store.OpenFile(versionsIndexName, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
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
	return prune(store)
}

// openStore opens the store at dir, <root>/.nodwyn/versions, making what is
// missing of it. Whoever can commit to a repository writes its .nodwyn, and
// git keeps links: a linked .nodwyn, versions or objects would put the store,
// and every object nodwyn writes into it, wherever the link names, outside the
// repository included. Each must be a folder, and the store is opened as the
// folder that was looked at, so what is written stays in it.
func openStore(dir string) (*os.Root, error) {
	root, err := os.OpenRoot(filepath.Dir(filepath.Dir(dir)))
	if err != nil {
		return nil, err
	}
	defer root.Close()
	name := filepath.Join(filepath.Base(filepath.Dir(dir)), filepath.Base(dir))
	if _, err := ownFolder(root, filepath.Dir(name)); err != nil {
		return nil, err
	}
	info, err := ownFolder(root, name)
	if err != nil {
		return nil, err
	}
	store, err := root.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	if opened, err := store.Stat("."); err != nil || !os.SameFile(opened, info) {
		store.Close()
		return nil, fmt.Errorf("versions: %s changed while it was opened", dir)
	}
	if _, err := ownFolder(store, "objects"); err != nil {
		store.Close()
		return nil, err
	}
	return store, nil
}

// ownFolder makes name in r if it is missing, and refuses it if it is there
// and is not a folder: a link is refused, not followed, as core's ownDir does
// for worktrees and attachments.
func ownFolder(r *os.Root, name string) (fs.FileInfo, error) {
	if err := r.Mkdir(name, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, err
	}
	info, err := r.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a folder: nodwyn keeps versions only in a folder of the repository's own", filepath.Join(r.Name(), name))
	}
	return info, nil
}

// regularIfThere refuses name in the store if it is there and is not a
// regular file. os.Root follows a link that stays inside it; the store's
// files are written only as themselves.
func regularIfThere(store *os.Root, name string) error {
	info, err := store.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a file: nodwyn writes no kept version through a link", filepath.Join(store.Name(), name))
	}
	return nil
}

// lockStore holds an advisory lock on the store, against the other sessions
// and daemons that use it. The lock is on the file description, so it also
// excludes another Keep in this process.
func lockStore(store *os.Root) (func(), error) {
	// Made exclusively, or opened as the file already there: openat with
	// O_CREAT alone, which os.Root uses, fails with ENOENT on macOS when two
	// sessions make the same new file at once.
	f, err := store.OpenFile("lock", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if errors.Is(err, fs.ErrExist) {
		if err = regularIfThere(store, "lock"); err == nil {
			f, err = store.OpenFile("lock", os.O_RDWR, 0)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("versions: lock: %w", err)
	}
	fd := int(f.Fd())
	for {
		if err = syscall.Flock(fd, syscall.LOCK_EX); !errors.Is(err, syscall.EINTR) {
			break
		}
	}
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("versions: lock: %w", err)
	}
	return func() { _ = syscall.Flock(fd, syscall.LOCK_UN); _ = f.Close() }, nil
}

// prune keeps the newest maxVersionsPer versions of each path and at most
// maxVersionsTotal bytes of objects, removing objects nothing refers to. The
// store is locked.
func prune(store *os.Root) error {
	all, err := readIndex(store.FS())
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
	// Whatever is at the temporary name goes, a link included, so the index
	// is written to a file of its own.
	tmp := versionsIndexName + ".tmp"
	if err := store.Remove(tmp); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	f, err := store.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(buf.Bytes())
	if err := errors.Join(werr, f.Close()); err != nil {
		return err
	}
	if err := store.Rename(tmp, versionsIndexName); err != nil {
		return err
	}
	for _, x := range all {
		if _, ok := sizes[x.Hash]; !ok {
			_ = store.Remove(filepath.Join("objects", x.Hash))
		}
	}
	return nil
}

func readIndex(store fs.FS) ([]Version, error) {
	f, err := store.Open(versionsIndexName)
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
	all, err := readIndex(os.DirFS(dir))
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
