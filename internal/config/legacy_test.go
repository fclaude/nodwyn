package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMigrateLegacy(t *testing.T) {
	base := t.TempDir()
	for _, dir := range []string{"config/nibb", "data/nibb/sessions"} {
		if err := os.MkdirAll(filepath.Join(base, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(base, "config/nibb/config.toml"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The data directory already exists under the new name: it stays, and
	// the former one is left alone.
	if err := os.MkdirAll(filepath.Join(base, "data/nodwyn"), 0o700); err != nil {
		t.Fatal(err)
	}
	p := PathsUnder(filepath.Join(base, "config/nodwyn"), filepath.Join(base, "data/nodwyn"))
	moved, err := MigrateLegacy(p)
	if err != nil || len(moved) != 1 || moved[0] != p.ConfigDir {
		t.Fatalf("moved %v, %v", moved, err)
	}
	if _, err := os.Stat(p.ConfigFile); err != nil {
		t.Fatal("config not moved:", err)
	}
	if _, err := os.Stat(filepath.Join(base, "data/nibb/sessions")); err != nil {
		t.Fatal("data moved over an existing directory")
	}
	if moved, err := MigrateLegacy(p); err != nil || len(moved) != 0 {
		t.Fatalf("second run moved %v, %v", moved, err)
	}
	// Explicit directories with other names are never touched.
	other := PathsUnder(filepath.Join(base, "custom"), filepath.Join(base, "data/nibb"))
	if moved, _ := MigrateLegacy(other); len(moved) != 0 {
		t.Fatalf("moved %v", moved)
	}
}

func TestMigrateFolder(t *testing.T) {
	root := t.TempDir()
	if ok, err := MigrateFolder(root); ok || err != nil {
		t.Fatal("nothing to move", ok, err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".nibb"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".nibb", "init.scm"), []byte(";;"), 0o600); err != nil {
		t.Fatal(err)
	}
	if ok, err := MigrateFolder(root); !ok || err != nil {
		t.Fatal("not moved", ok, err)
	}
	if _, err := os.Stat(filepath.Join(root, ".nodwyn", "init.scm")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".nibb"), 0o700); err != nil {
		t.Fatal(err)
	}
	if ok, _ := MigrateFolder(root); ok {
		t.Fatal("moved over an existing .nodwyn")
	}
}
