package prim

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/fclaude/nodwyn/internal/guard"
	"github.com/fclaude/nodwyn/internal/scheme"
)

// Keeper keeps a git work tree's uncommitted changes as they stood at the
// latest test run that passed, and puts them back (ADR 18, amended
// 2026-10-06). An unattended turn that is stopped by a hard limit, or by the
// progress guard, is stopped where it happens to be, which is often one edit
// past the last state its own tests were happy with; nobody reads the work
// before it is collected, so what stays on disk should be the state the
// evidence supports. The files the turn changed since go back to it, and
// their later contents are saved beside it, so nothing the model wrote is
// lost.
//
// It works on files, not on git's objects or index: the changes against HEAD
// as git status lists them (modified, new, deleted), each kept as the bytes
// on disk. Git is asked only for the list and for HEAD's blobs, as the git
// tools ask it, with nothing that runs another program. What it never keeps
// or touches: a file the credential denylist refuses, a path under one the
// guard protects (.nodwyn/, .git/, Makefile, scripts/ and the rest, where a
// write needs a security confirm), links, and a file over keepMaxFile. The
// index is not changed.
type Keeper struct {
	o      Options
	mu     sync.Mutex
	failed string // why the latest Keep did not keep, "" when it did
}

// NewKeeper is a Keeper for the repository o names, keeping what it keeps in
// o's scratch directory.
func NewKeeper(o Options) *Keeper { return &Keeper{o: o} }

const (
	keepMaxFile  = 8 << 20  // a larger file is not kept, and so nothing is
	keepMaxTotal = 64 << 20 // all the files together
	keepMaxFiles = 4000
)

// ErrNothingKept is Restore's answer when no passing state was kept.
var ErrNothingKept = errors.New("no passing state was kept")

// keptFile is one path as it stood: its bytes (named under blobs/), or gone.
type keptFile struct {
	Path   string `json:"path"` // slash-separated, relative to the root
	Absent bool   `json:"absent,omitempty"`
	Mode   uint32 `json:"mode,omitempty"`
	Blob   string `json:"blob,omitempty"`
}

type keptState struct {
	Head  string     `json:"head"`
	Files []keptFile `json:"files"`
}

func (k *Keeper) dir() string { return filepath.Join(k.o.Scratch, "kept") }

// Failed is why the latest Keep kept nothing, or "".
func (k *Keeper) Failed() string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.failed
}

// Keep replaces what is kept with the changes as they are now, and returns
// how many files it kept. A Keep that fails leaves the earlier one, which
// was a state a test run passed on too.
func (k *Keeper) Keep(ctx context.Context) (int, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	n, err := k.keep(ctx)
	k.failed = ""
	if err != nil {
		k.failed = err.Error()
	}
	return n, err
}

func (k *Keeper) keep(ctx context.Context) (int, error) {
	head, paths, err := k.look(ctx)
	if err != nil {
		return 0, err
	}
	if len(paths) > keepMaxFiles {
		return 0, fmt.Errorf("%d files changed: more than the %d that are kept", len(paths), keepMaxFiles)
	}
	deny := denylist(k.o)
	tmp := k.dir() + ".new"
	if err := os.RemoveAll(tmp); err != nil {
		return 0, err
	}
	if err := os.MkdirAll(filepath.Join(tmp, "blobs"), 0o700); err != nil {
		return 0, err
	}
	state := keptState{Head: head}
	var total int64
	for i, rel := range paths {
		if !keepable(deny, rel) {
			continue
		}
		abs := filepath.Join(k.o.Root, filepath.FromSlash(rel))
		info, err := os.Lstat(abs)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			state.Files = append(state.Files, keptFile{Path: rel, Absent: true})
		case err != nil:
			return 0, err
		case !info.Mode().IsRegular():
			// A link, or a directory such as a submodule's: left as it is.
		default:
			if total += info.Size(); info.Size() > keepMaxFile || total > keepMaxTotal {
				return 0, fmt.Errorf("%s makes the changes too large to keep (a file over %d MiB, or more than %d MiB in all)", rel, keepMaxFile>>20, keepMaxTotal>>20)
			}
			data, err := os.ReadFile(abs)
			if err != nil {
				return 0, err
			}
			name := strconv.Itoa(i)
			if err := os.WriteFile(filepath.Join(tmp, "blobs", name), data, 0o600); err != nil {
				return 0, err
			}
			state.Files = append(state.Files, keptFile{Path: rel, Mode: uint32(info.Mode().Perm()), Blob: name})
		}
	}
	index, err := json.Marshal(state)
	if err != nil {
		return 0, err
	}
	if err := os.WriteFile(filepath.Join(tmp, "state.json"), index, 0o600); err != nil {
		return 0, err
	}
	if err := os.RemoveAll(k.dir()); err != nil {
		return 0, err
	}
	if err := os.Rename(tmp, k.dir()); err != nil {
		return 0, err
	}
	return len(state.Files), nil
}

// keepable is whether a path is one the keeper may read and write.
func keepable(deny guard.PathDenylist, rel string) bool {
	if rel == "" || filepath.IsAbs(rel) || guard.IsProtected(rel) || deny.Denies(rel) != "" {
		return false
	}
	for _, part := range strings.Split(rel, "/") {
		if part == ".." {
			return false
		}
	}
	return true
}

// look is HEAD and the paths git lists as changed, after checking that the
// root is the repository's top and has a commit, since the paths git lists
// are the top's.
func (k *Keeper) look(ctx context.Context) (string, []string, error) {
	if k.o.Scratch == "" || !isGitWorkTree(k.o.Root) {
		return "", nil, errors.New("not a git work tree with a scratch directory")
	}
	top, err := runGit(ctx, k.o, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", nil, err
	}
	if !sameDir(strings.TrimSpace(top), k.o.Root) {
		return "", nil, errors.New("the root is not the top of its git repository")
	}
	head, err := runGit(ctx, k.o, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return "", nil, errors.New("the repository has no commit yet")
	}
	out, err := runGitLimit(ctx, k.o, 16<<20, "status", "--porcelain=v1", "-z", "--no-renames", "--untracked-files=all")
	if err != nil {
		return "", nil, err
	}
	var paths []string
	for _, rec := range strings.Split(out, "\x00") {
		if len(rec) > 3 {
			paths = append(paths, rec[3:])
		}
	}
	return strings.TrimSpace(head), paths, nil
}

func sameDir(a, b string) bool {
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
}

// Restored is what Restore did.
type Restored struct {
	Put       int      // files written back
	Removed   int      // files taken away
	Skipped   []string // paths it could not put back, with why
	Discarded string   // where the contents it replaced were saved
}

// Restore puts every file that differs from the kept state back to it: a
// kept file as it was, a path changed since and not kept as HEAD has it (or
// gone, when HEAD does not have it). What it replaces is saved first, under
// Discarded, in the same relative paths. It refuses when HEAD is not the
// commit the state was kept on, since a commit in between changes what
// "unchanged" means.
func (k *Keeper) Restore(ctx context.Context) (Restored, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	var res Restored
	raw, err := os.ReadFile(filepath.Join(k.dir(), "state.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return res, ErrNothingKept
	}
	if err != nil {
		return res, err
	}
	var state keptState
	if err := json.Unmarshal(raw, &state); err != nil {
		return res, fmt.Errorf("the kept state cannot be read: %w", err)
	}
	head, now, err := k.look(ctx)
	if err != nil {
		return res, err
	}
	if head != state.Head {
		return res, fmt.Errorf("HEAD is %.12s, not %.12s the state was kept on: nothing was put back", head, state.Head)
	}
	deny := denylist(k.o)
	res.Discarded = filepath.Join(k.dir(), "discarded")
	kept := map[string]bool{}
	for _, f := range state.Files {
		kept[f.Path] = true
	}
	var tracked map[string]headFile
	for _, rel := range now {
		if !kept[rel] && keepable(deny, rel) {
			if tracked == nil {
				if tracked, err = k.headFiles(ctx); err != nil {
					return res, err
				}
			}
			h, inHead := tracked[rel]
			f := keptFile{Path: rel, Absent: !inHead}
			if inHead {
				f.Mode = h.mode
			}
			if err := k.put(ctx, &res, f, func() ([]byte, error) { return k.headBlob(ctx, h.oid) }); err != nil {
				return res, err
			}
		}
	}
	for _, f := range state.Files {
		if !keepable(deny, f.Path) {
			continue
		}
		f := f
		if err := k.put(ctx, &res, f, func() ([]byte, error) {
			return os.ReadFile(filepath.Join(k.dir(), "blobs", f.Blob))
		}); err != nil {
			return res, err
		}
	}
	return res, nil
}

// put makes one path what f says, saving what it replaces. A path it cannot
// make so (a link in the way, a directory that leaves the root) is left and
// named in res.Skipped.
func (k *Keeper) put(_ context.Context, res *Restored, f keptFile, content func() ([]byte, error)) error {
	abs := filepath.Join(k.o.Root, filepath.FromSlash(f.Path))
	skip := func(why string) error { res.Skipped = append(res.Skipped, f.Path+" ("+why+")"); return nil }
	if !k.inside(abs) {
		return skip("outside the repository")
	}
	info, err := os.Lstat(abs)
	exists := err == nil
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if exists && !info.Mode().IsRegular() {
		return skip("not a regular file")
	}
	var current []byte
	if exists {
		if current, err = os.ReadFile(abs); err != nil {
			return err
		}
	}
	if f.Absent {
		if !exists {
			return nil
		}
		if err := k.save(f.Path, current); err != nil {
			return err
		}
		if err := os.Remove(abs); err != nil {
			return err
		}
		res.Removed++
		k.pruneDirs(abs)
		return nil
	}
	want, err := content()
	if err != nil {
		return skip("not readable: " + err.Error())
	}
	mode := fs.FileMode(f.Mode) & 0o777
	if mode == 0 {
		mode = 0o644
	}
	if exists && bytes.Equal(current, want) && info.Mode().Perm()&0o111 == mode&0o111 {
		return nil
	}
	if exists {
		if err := k.save(f.Path, current); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	if !k.inside(abs) {
		return skip("outside the repository")
	}
	tmp, err := os.CreateTemp(filepath.Dir(abs), ".nodwyn-kept-*")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(want)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		os.Remove(tmp.Name())
		return errors.Join(werr, cerr)
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), abs); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	res.Put++
	return nil
}

// save keeps what a restore replaces, in the same relative path under the
// kept directory's discarded folder.
func (k *Keeper) save(rel string, data []byte) error {
	dst := filepath.Join(k.dir(), "discarded", filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o600)
}

// inside is whether abs is still under the root once the links on its way
// are followed: a directory the model replaced with a link to somewhere else
// must not take a write.
func (k *Keeper) inside(abs string) bool {
	root, err := filepath.EvalSymlinks(k.o.Root)
	if err != nil {
		return false
	}
	for dir := filepath.Dir(abs); ; dir = filepath.Dir(dir) {
		if resolved, err := filepath.EvalSymlinks(dir); err == nil {
			rel, err := filepath.Rel(root, resolved)
			return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
		}
		if dir == filepath.Dir(dir) {
			return false
		}
	}
}

// pruneDirs removes the directories a removed file left empty, up to the
// root.
func (k *Keeper) pruneDirs(abs string) {
	for dir := filepath.Dir(abs); dir != k.o.Root && strings.HasPrefix(dir, k.o.Root+string(filepath.Separator)); dir = filepath.Dir(dir) {
		if os.Remove(dir) != nil {
			return
		}
	}
}

type headFile struct {
	oid  string
	mode uint32
}

// headFiles is every regular file HEAD has, by path. A link or a submodule is
// left out, so a path that is one is named as skipped.
func (k *Keeper) headFiles(ctx context.Context) (map[string]headFile, error) {
	out, err := runGitLimit(ctx, k.o, 128<<20, "ls-tree", "-r", "-z", "HEAD")
	if err != nil {
		return nil, err
	}
	files := map[string]headFile{}
	for _, rec := range strings.Split(out, "\x00") {
		meta, path, ok := strings.Cut(rec, "\t")
		fields := strings.Fields(meta)
		if !ok || len(fields) != 3 || fields[1] != "blob" || fields[0] == "120000" {
			continue
		}
		mode := uint32(0o644)
		if fields[0] == "100755" {
			mode = 0o755
		}
		files[path] = headFile{oid: fields[2], mode: mode}
	}
	return files, nil
}

func (k *Keeper) headBlob(ctx context.Context, oid string) ([]byte, error) {
	if oid == "" {
		return nil, errors.New("HEAD does not have it as a file")
	}
	out, err := runGitLimit(ctx, k.o, keepMaxFile+1, "cat-file", "blob", oid)
	if err != nil {
		return nil, err
	}
	if len(out) > keepMaxFile {
		return nil, fmt.Errorf("over %d MiB", keepMaxFile>>20)
	}
	return []byte(out), nil
}

// keepPassing is (keep-passing): the evidence loop calls it when a test run
// after the last edit has passed, in a turn nobody reads. Without a Keeper
// there is nothing to keep, and it says #f.
func keepPassing(o Options) *scheme.Prim {
	return &scheme.Prim{
		Name: "keep-passing",
		Meta: scheme.Meta{
			Doc:    "Keep the repository's uncommitted changes as they are now, as a state a test run passed on: an unattended turn that a hard limit or the progress guard stops has the files it changed since put back to it, and what it replaces is saved beside it. Returns how many files were kept, or #f where nothing is kept (no Keeper), and an error saying why when it could not keep.",
			Host:   true,
			Hidden: true,
		},
		Handler: func(c *scheme.Call) (scheme.Value, error) {
			if o.Keeper == nil {
				return scheme.False, nil
			}
			n, err := o.Keeper.Keep(c.Ctx)
			if err != nil {
				return scheme.Value{}, fmt.Errorf("keep-passing: %w", err)
			}
			return scheme.Int(int64(n)), nil
		},
	}
}
