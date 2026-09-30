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
	// Inside a git work tree, git is the undo.
	os.Mkdir(filepath.Join(root, ".git"), 0o755)
	git := &Versions{Dir: VersionsDir(root), Skip: InGitWorkTree(root)}
	before, _ := ListVersions(git.Dir, file)
	git.Keep(file)
	after, _ := ListVersions(git.Dir, file)
	if len(after) != len(before) {
		t.Fatal("versioned a file git already covers")
	}
	var nilStore *Versions
	if err := nilStore.Keep(file); err != nil {
		t.Fatal(err)
	}
}
