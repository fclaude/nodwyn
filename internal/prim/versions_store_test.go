package prim

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/fclaude/nodwyn/internal/scheme"
	"github.com/fclaude/nodwyn/test/fakes/must"
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

// A repository writes its own .nodwyn, and git keeps links. Keep followed a
// link anywhere in the store: one at objects/<hash of a file's content>
// created the file it named, outside the repository, with that content, and a
// linked .nodwyn, versions or objects put the whole store elsewhere. Each is
// now refused, or not followed, and nothing is written outside.
func TestKeepWritesNothingThroughALinkInTheStore(t *testing.T) {
	content := []byte("export NODWYN=1\n")
	sum := sha256.Sum256(content)
	hash := hex.EncodeToString(sum[:])
	for _, c := range []struct {
		link   string // under the root
		folder bool   // whether it names the outside folder, else a file in it
		fails  bool   // whether Keep refuses, else it goes on without the link
	}{
		{".nodwyn", true, true},
		{".nodwyn/versions", true, true},
		{".nodwyn/versions/objects", true, true},
		{".nodwyn/versions/objects/" + hash, false, true},
		{".nodwyn/versions/index.jsonl", false, true},
		{".nodwyn/versions/lock", false, true},
		{".nodwyn/versions/.gitignore", false, false},
	} {
		t.Run(c.link, func(t *testing.T) {
			root, outside := t.TempDir(), t.TempDir()
			file := filepath.Join(root, "notes.txt")
			must.WriteFile(t, file, content, 0o644)
			link := filepath.Join(root, filepath.FromSlash(c.link))
			must.MkdirAll(t, filepath.Dir(link), 0o700)
			target := outside
			if !c.folder {
				target = filepath.Join(outside, "planted")
			}
			must.Symlink(t, target, link)
			err := (&Versions{Dir: VersionsDir(root)}).Keep(file)
			if c.fails && err == nil {
				t.Error("kept a version through a link")
			}
			if !c.fails && err != nil {
				t.Errorf("a link that is not followed refused the version: %v", err)
			}
			if entries, _ := os.ReadDir(outside); len(entries) != 0 {
				t.Errorf("wrote outside the repository: %s", entries[0].Name())
			}
		})
	}
}

// Pruning rewrote the index through index.jsonl.tmp, following a link put
// there to write the index wherever it named.
func TestPruneWritesNothingThroughALinkedIndex(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	v := &Versions{Dir: VersionsDir(root)}
	file := filepath.Join(root, "notes.txt")
	for i := 0; i < maxVersionsPer; i++ {
		must.WriteFile(t, file, []byte(fmt.Sprintf("version %d", i)), 0o644)
		if err := v.Keep(file); err != nil {
			t.Fatal(err)
		}
	}
	must.Symlink(t, filepath.Join(outside, "planted"), filepath.Join(v.Dir, versionsIndexName+".tmp"))
	must.WriteFile(t, file, []byte("one more"), 0o644)
	if err := v.Keep(file); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatalf("wrote outside the repository: %s", entries[0].Name())
	}
	if list, err := ListVersions(v.Dir, file); err != nil || len(list) != maxVersionsPer {
		t.Fatalf("versions after pruning: %d, %v", len(list), err)
	}
}

// What the person sees: approving a write to a file of a hostile repository
// fails, and neither the file nor anything outside the repository changes.
func TestFsWriteFailsWhenTheStoreLinksOutside(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	content := []byte("export NODWYN=1\n")
	sum := sha256.Sum256(content)
	must.WriteFile(t, filepath.Join(root, "notes.txt"), content, 0o644)
	objects := filepath.Join(VersionsDir(root), "objects")
	must.MkdirAll(t, objects, 0o700)
	must.Symlink(t, filepath.Join(outside, ".zshenv"), filepath.Join(objects, hex.EncodeToString(sum[:])))
	o := DefaultOptions(root)
	o.Versions = &Versions{Dir: VersionsDir(root)}
	in := scheme.New()
	if err := Register(in, o); err != nil {
		t.Fatal(err)
	}
	p, _ := in.Lookup("fs-write")
	args := []scheme.Value{scheme.Str("notes.txt"), scheme.Str("changed\n")}
	if _, err := in.Apply(context.Background(), scheme.Value{Kind: scheme.KPrim, Prim: p}, args); err == nil || !strings.Contains(err.Error(), "nothing was written") {
		t.Fatalf("got %v", err)
	}
	if got := readFile(t, filepath.Join(root, "notes.txt")); got != string(content) {
		t.Fatalf("the file was written: %q", got)
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatalf("wrote outside the repository: %s", entries[0].Name())
	}
}
