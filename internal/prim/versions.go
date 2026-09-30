package prim

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Versions keeps a file's previous content before nodwyn replaces it, where
// git is not the undo (ADR 22 §2): content-addressed objects under
// <root>/.nodwyn/versions/objects and an append-only index. It is capped per
// file and in total; the oldest versions go first.
type Versions struct {
	Dir string // <root>/.nodwyn/versions
	// Skip reports paths another undo already covers (a git work tree).
	Skip func(abs string) bool
	// Record is told of every kept version, for the session log.
	Record func(Version)

	mu sync.Mutex
}

// Version is one kept previous content of a file.
type Version struct {
	Path  string    `json:"path"` // absolute
	Hash  string    `json:"hash"` // sha256 of the content
	Bytes int       `json:"bytes"`
	Time  time.Time `json:"time"`
}

const (
	maxVersionFile    = 16 << 20  // larger files are not kept
	maxVersionsPer    = 20        // per path
	maxVersionsTotal  = 256 << 20 // all objects together
	versionsIndexName = "index.jsonl"
)

var hashName = regexp.MustCompile(`^[0-9a-f]{64}$`)

// VersionsDir is where root's versions live.
func VersionsDir(root string) string { return filepath.Join(root, ".nodwyn", "versions") }

// Keep saves abs's current content, if it has any and it is not covered by
// another undo. A failure to keep a version fails the write: a workspace
// that promises an undo must not lose one silently.
func (v *Versions) Keep(abs string) error {
	if v == nil || (v.Skip != nil && v.Skip(abs)) {
		return nil
	}
	info, err := os.Lstat(abs)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > maxVersionFile {
		return nil
	}
	content, err := os.ReadFile(abs)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(content)
	version := Version{Path: abs, Hash: hex.EncodeToString(sum[:]), Bytes: len(content), Time: time.Now().UTC()}
	v.mu.Lock()
	defer v.mu.Unlock()
	objects := filepath.Join(v.Dir, "objects")
	if err := os.MkdirAll(objects, 0o700); err != nil {
		return err
	}
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

// prune keeps the newest maxVersionsPer versions of each path and at most
// maxVersionsTotal bytes of objects, removing objects nothing refers to.
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
		if all[i].Path == abs {
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
	content, err := os.ReadFile(filepath.Join(dir, "objects", hash))
	if err != nil {
		return nil, err
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

// InGitWorkTree reports whether path is inside a git work tree whose top
// is root or below it: there git is the undo.
func InGitWorkTree(root string) func(string) bool {
	_, err := os.Stat(filepath.Join(root, ".git"))
	git := err == nil
	return func(abs string) bool {
		return git && (abs == root || strings.HasPrefix(abs, root+string(filepath.Separator)))
	}
}
