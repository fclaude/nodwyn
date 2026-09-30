package config

import (
	"errors"
	"os"
	"path/filepath"
)

// LegacyName is what nodwyn was called until 2026-09-30 (ADR 31). Folders
// kept under that name move to the new one, once, the first time they are
// used; nothing moves when the new name already exists.
const LegacyName = "nibb"

// FolderDir is the directory nodwyn keeps inside a folder it works in.
const FolderDir = ".nodwyn"

// MigrateLegacy moves the config and data directories kept under the
// former name to p's, each only when p's does not exist yet. It applies to
// the default layout only (directories named "nodwyn"), and reports the
// directories it moved.
func MigrateLegacy(p Paths) ([]string, error) {
	var moved []string
	for _, dir := range []string{p.ConfigDir, p.DataDir} {
		if filepath.Base(dir) != "nodwyn" {
			continue
		}
		ok, err := moveIfAbsent(filepath.Join(filepath.Dir(dir), LegacyName), dir)
		if err != nil {
			return moved, err
		}
		if ok {
			moved = append(moved, dir)
		}
	}
	return moved, nil
}

// MigrateFolder renames root/.nibb to root/.nodwyn when only the former
// exists, and reports whether it did.
func MigrateFolder(root string) (bool, error) {
	return moveIfAbsent(filepath.Join(root, "."+LegacyName), filepath.Join(root, FolderDir))
}

func moveIfAbsent(from, to string) (bool, error) {
	if _, err := os.Lstat(to); err == nil || !errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	info, err := os.Lstat(from)
	if err != nil || !info.IsDir() {
		return false, nil
	}
	if err := os.Rename(from, to); err != nil {
		return false, err
	}
	return true, nil
}
