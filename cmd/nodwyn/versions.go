package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/fclaude/nodwyn/internal/prim"
)

// versions lists the kept previous versions of a file (ADR 22 §2).
func (c *cli) versions() error {
	root, abs, err := c.versionTarget("versions")
	if err != nil {
		return err
	}
	list, err := prim.ListVersions(prim.VersionsDir(root), abs)
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
	dir := prim.VersionsDir(root)
	list, err := prim.ListVersions(dir, abs)
	if err != nil {
		return err
	}
	if len(list) == 0 {
		return fmt.Errorf("undo: no kept version of %s", abs)
	}
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
