package prim

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fs-write follows a link and replaces its target, so the version kept is the
// target's: Keep used to find a link and keep nothing, and the target's old
// content was gone.
func TestKeepFollowsALinkToItsTarget(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "real.txt")
	link := filepath.Join(root, "link.txt")
	if err := os.WriteFile(target, []byte("before"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real.txt", link); err != nil {
		t.Fatal(err)
	}
	v := &Versions{Dir: VersionsDir(root)}
	if err := v.Keep(link); err != nil {
		t.Fatal(err)
	}
	list, err := ListVersions(v.Dir, target)
	if err != nil || len(list) != 1 {
		t.Fatalf("versions of the target: %d, %v", len(list), err)
	}
	if got, err := ReadVersion(v.Dir, list[0].Hash); err != nil || string(got) != "before" {
		t.Fatalf("kept %q, %v", got, err)
	}
}

// A file too large to keep is not overwritten without a version: the write is
// refused, saying so, where it used to go ahead and lose the content.
func TestKeepRefusesAFileTooLargeToKeep(t *testing.T) {
	root := t.TempDir()
	big := filepath.Join(root, "big.bin")
	if err := os.WriteFile(big, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(big, maxVersionFile+1); err != nil {
		t.Fatal(err)
	}
	err := (&Versions{Dir: VersionsDir(root)}).Keep(big)
	if err == nil || !strings.Contains(err.Error(), "a kept version holds") {
		t.Fatalf("got %v", err)
	}
}

// The sessions of one repository each have a Versions of their own over one
// store. A pruning session rewrote the index from what it had read, so a
// version another appended meanwhile vanished and its object stayed behind.
func TestVersionsOfSeveralSessionsLoseNoneAndLeaveNoOrphan(t *testing.T) {
	root := t.TempDir()
	const sessions, writes = 6, maxVersionsPer + 10
	var wg sync.WaitGroup
	errs := make(chan error, sessions)
	for s := 0; s < sessions; s++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v := &Versions{Dir: VersionsDir(root)}
			file := filepath.Join(root, fmt.Sprintf("file%d.txt", s))
			for i := 0; i < writes; i++ {
				if err := os.WriteFile(file, []byte(fmt.Sprintf("session %d version %d", s, i)), 0o644); err != nil {
					errs <- err
					return
				}
				if err := v.Keep(file); err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	referenced := map[string]bool{}
	for s := 0; s < sessions; s++ {
		list, err := ListVersions(VersionsDir(root), filepath.Join(root, fmt.Sprintf("file%d.txt", s)))
		if err != nil || len(list) != maxVersionsPer {
			t.Fatalf("session %d: %d versions listed, want %d (%v)", s, len(list), maxVersionsPer, err)
		}
		for _, x := range list {
			referenced[x.Hash] = true
			if _, err := ReadVersion(VersionsDir(root), x.Hash); err != nil {
				t.Fatalf("a listed version has no object: %v", err)
			}
		}
	}
	objects, _ := os.ReadDir(filepath.Join(VersionsDir(root), "objects"))
	for _, o := range objects {
		if !referenced[o.Name()] {
			t.Errorf("object %s is listed by no version", o.Name()[:12])
		}
	}
}
