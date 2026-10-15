package core

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/retrieval"
)

// IndexInfo is what a person is shown of a project's repository index (ADR
// 50): how many files the folder's own tree holds, for how many working
// trees the project keeps one (its worktrees'), its size on disk, and when
// the folder's tree last changed. The zero value is no index.
type IndexInfo struct {
	Files   int       `json:"files"`
	Trees   int       `json:"trees"`
	Bytes   int64     `json:"bytes"`
	Updated time.Time `json:"updated,omitzero"`
}

// indexDir is where the project at repo keeps its repository index, beside
// its notes; "" without a data directory.
func indexDir(dataDir, repo string) string {
	if dir := projectDir(dataDir, repo); dir != "" {
		return filepath.Join(dir, "index")
	}
	return ""
}

// ProjectIndex describes the repository index of the project at repo, the
// repository's own folder for a task in one of its worktrees. It reads the
// manifests and sizes only.
func ProjectIndex(dataDir, repo string) (IndexInfo, error) {
	var info IndexInfo
	dir := indexDir(dataDir, repo)
	if dir == "" {
		return info, nil
	}
	trees, _ := filepath.Glob(filepath.Join(dir, "trees", "*.json"))
	info.Trees = len(trees)
	store := retrieval.Store{Dir: dir}
	root := config.CanonicalPath(repo)
	path := store.ManifestPath(root)
	if raw, err := os.ReadFile(path); err == nil {
		info.Files = len(retrieval.ParseManifest(raw, root).Files)
		if st, err := os.Stat(path); err == nil {
			info.Updated = st.ModTime()
		}
	}
	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if st, err := d.Info(); err == nil && !d.IsDir() {
			info.Bytes += st.Size()
		}
		return nil
	})
	return info, err
}

// ForgetProjectIndex deletes the project's repository index. It is derived
// from the files (SPEC, invariant 1): the next search builds it again.
func ForgetProjectIndex(dataDir, repo string) error {
	dir := indexDir(dataDir, repo)
	if dir == "" {
		return nil
	}
	return os.RemoveAll(dir)
}
