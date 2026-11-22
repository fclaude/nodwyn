package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/prim"
)

// versions lists the kept previous versions of a file (ADR 22 §2).
func (c *cli) versions() error {
	root, abs, err := c.versionTarget("versions")
	if err != nil {
		return err
	}
	list, _, err := keptVersions(root, abs)
	if err != nil {
		return err
	}
	if len(list) == 0 {
		fmt.Println("No kept versions of", abs)
		return nil
	}
	for _, v := range list {
		fmt.Printf("%s  %s  %6d bytes\n", v.Time.Local().Format("2006-01-02 15:04:05"), v.Hash[:12], v.Bytes)
	}
	return nil
}

// undo puts back a file's newest kept version. The current content is kept
// first, so an undo can itself be undone.
func (c *cli) undo() error {
	root, abs, err := c.versionTarget("undo")
	if err != nil {
		return err
	}
	list, dirs, err := keptVersions(root, abs)
	if err != nil {
		return err
	}
	if len(list) == 0 {
		return fmt.Errorf("undo: no kept version of %s", abs)
	}
	dir := dirs[0]
	content, err := prim.ReadVersion(dir, list[0].Hash)
	if err != nil {
		return err
	}
	store := &prim.Versions{Dir: dir}
	if err := store.Keep(abs); err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if info, err := os.Stat(abs); err == nil {
		mode = info.Mode().Perm()
	}
	if err := os.WriteFile(abs, content, mode); err != nil {
		return err
	}
	fmt.Printf("Restored %s to its version of %s (%s).\n", abs, list[0].Time.Local().Format("2006-01-02 15:04"), list[0].Hash[:12])
	return nil
}

// versionTarget reads PATH (relative to the working directory) and the
// workspace root (--repo, default the working directory).
func (c *cli) versionTarget(name string) (root, abs string, err error) {
	if err := c.only(name, "repo"); err != nil {
		return "", "", err
	}
	args := c.rest()
	if len(args) != 1 {
		return "", "", fmt.Errorf("%s: expected one file path", name)
	}
	root, err = filepath.Abs(c.repo)
	if err != nil {
		return "", "", err
	}
	abs, err = filepath.Abs(args[0])
	return filepath.Clean(root), filepath.Clean(abs), err
}

// keptVersions are abs's kept versions, newest first, from every store they
// may be in, with the store each is in. A task in a worktree keeps them in
// its repository's store, not the worktree's (ADR 34), so run in the
// worktree, as the task's conversation says to work, the folder's own store
// had none of them; the folder's store, its git top folder's and, for a
// linked worktree, its repository's are all read.
func keptVersions(root, abs string) ([]prim.Version, []string, error) {
	type kept struct {
		v   prim.Version
		dir string
	}
	var all []kept
	for _, store := range versionStores(root) {
		found, err := prim.ListVersions(prim.VersionsDir(store), abs)
		if err != nil {
			return nil, nil, err
		}
		for _, v := range found {
			all = append(all, kept{v, prim.VersionsDir(store)})
		}
	}
	slices.SortStableFunc(all, func(a, b kept) int { return b.v.Time.Compare(a.v.Time) })
	list, dirs := make([]prim.Version, len(all)), make([]string, len(all))
	for i, k := range all {
		list[i], dirs[i] = k.v, k.dir
	}
	return list, dirs, nil
}

// versionStores are the folders whose stores root's files' versions may be
// in: root, its git work tree's top folder, and the repository a linked
// worktree belongs to, each once. git is asked only for paths, with nothing
// that runs another program.
func versionStores(root string) []string {
	stores := []string{config.CanonicalPath(root)}
	add := func(dir string) {
		if dir != "" && !slices.Contains(stores, dir) {
			stores = append(stores, dir)
		}
	}
	out, err := exec.Command("git", "-C", root, "rev-parse", "--path-format=absolute", "--show-toplevel", "--git-common-dir").Output()
	if err != nil {
		return stores
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 2 {
		return stores
	}
	add(config.CanonicalPath(lines[0]))
	if filepath.Base(lines[1]) == ".git" {
		add(config.CanonicalPath(filepath.Dir(lines[1])))
	}
	return stores
}
