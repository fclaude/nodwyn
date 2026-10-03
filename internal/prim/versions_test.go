package prim

import (
	"os"
	"path/filepath"
	"testing"
)

func TestVersionsKeepListReadAndPrune(t *testing.T) {
	root := t.TempDir()
	var recorded []Version
	v := &Versions{Dir: VersionsDir(root), Skip: InGitWorkTree(root), Record: func(x Version) { recorded = append(recorded, x) }}
	file := filepath.Join(root, "notes.md")
	if err := v.Keep(file); err != nil || len(recorded) != 0 {
		t.Fatal("kept a file that does not exist", err)
	}
	for i := 0; i < maxVersionsPer+5; i++ {
		os.WriteFile(file, []byte{byte('a' + i%26), byte(i)}, 0o644)
		if err := v.Keep(file); err != nil {
			t.Fatal(err)
		}
	}
	list, err := ListVersions(v.Dir, file)
	if err != nil || len(list) != maxVersionsPer || len(recorded) != maxVersionsPer+5 {
		t.Fatalf("versions: %d (recorded %d) %v", len(list), len(recorded), err)
	}
	newest, err := ReadVersion(v.Dir, list[0].Hash)
	if err != nil || newest[1] != byte(maxVersionsPer+4) {
		t.Fatalf("newest: %v %v", newest, err)
	}
	objects, _ := os.ReadDir(filepath.Join(v.Dir, "objects"))
	if len(objects) != maxVersionsPer {
		t.Fatalf("pruned objects: %d", len(objects))
	}
	if _, err := ReadVersion(v.Dir, "../../etc/passwd"); err == nil {
		t.Fatal("read outside the store")
	}
	os.WriteFile(filepath.Join(v.Dir, "objects", list[0].Hash), []byte("tampered"), 0o600)
	if _, err := ReadVersion(v.Dir, list[0].Hash); err == nil {
		t.Fatal("damaged object accepted")
	}
	var nilStore *Versions
	if err := nilStore.Keep(file); err != nil {
		t.Fatal(err)
	}
}

// In a git work tree git is the undo only for what it has committed: a
// tracked, unchanged file keeps no version, while an uncommitted change, an
// untracked file and an ignored one do (ADR 35).
func TestVersionsInGitKeepWhatGitCannotRestore(t *testing.T) {
	root, _ := gitRepo(t)
	os.WriteFile(filepath.Join(root, ".gitignore"), []byte("local.conf\n"), 0o600)
	os.WriteFile(filepath.Join(root, "local.conf"), []byte("mine\n"), 0o600)
	os.WriteFile(filepath.Join(root, "new.txt"), []byte("draft\n"), 0o600)
	v := &Versions{Dir: VersionsDir(root), Skip: InGitWorkTree(root)}
	kept := func(name string) bool {
		t.Helper()
		abs := filepath.Join(root, name)
		if err := v.Keep(abs); err != nil {
			t.Fatal(err)
		}
		list, err := ListVersions(v.Dir, abs)
		if err != nil {
			t.Fatal(err)
		}
		return len(list) > 0
	}
	if kept("plants.txt") {
		t.Fatal("kept a version of a committed, unchanged file")
	}
	os.WriteFile(filepath.Join(root, "plants.txt"), []byte("tomato\nbasil\n"), 0o600)
	for _, name := range []string{"plants.txt", "new.txt", "local.conf"} {
		if !kept(name) {
			t.Errorf("%s: no version kept of what git cannot restore", name)
		}
	}
	if InGitWorkTree(t.TempDir())(filepath.Join(root, "plants.txt")) {
		t.Fatal("skipped outside a git work tree")
	}
}
