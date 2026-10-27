package prim

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/fclaude/brote/scheme"
)

// gitContentState compares bytes on disk, not HEAD, the index or a diff's
// presentation. On 2026-10-07/08, committing new files with git-land changed
// the validation wrapper's status/diff fingerprint without changing content.
func gitContentState(o Options) *scheme.Prim {
	return &scheme.Prim{
		Name: "git-content-state",
		Meta: scheme.Meta{
			Doc:  "An opaque fingerprint of tracked and non-ignored untracked workspace files: paths, executable bits, bytes and link targets, never commit IDs or staging state. Files fs-read refuses are omitted; links are not followed. Errors (including submodules or size limits) leave validation unknown. Hidden: the repository's finish check uses it to retain passing validation across content-preserving git-land calls.",
			Host: true, Hidden: true,
		},
		Handler: func(c *scheme.Call) (scheme.Value, error) {
			state, err := contentState(c.Ctx, o)
			return scheme.Str(state), err
		},
	}
}

func contentState(ctx context.Context, o Options) (string, error) {
	const listingLimit = 16 << 20
	listing, err := runGitLimit(ctx, o, listingLimit, "ls-files", "--cached", "--others", "--exclude-standard", "-z")
	if err != nil {
		return "", err
	}
	if len(listing) >= listingLimit {
		return "", errors.New("workspace file listing is too large to fingerprint")
	}
	root, err := os.OpenRoot(o.Root)
	if err != nil {
		return "", err
	}
	defer root.Close()
	names := strings.Split(listing, "\x00")
	sort.Strings(names)
	deny := denylist(o)
	h := sha256.New()
	var used int64
	previous := ""
	for _, name := range names {
		if name == "" || name == previous || deny.Denies(name) != "" {
			continue
		}
		previous = name
		if err := ctx.Err(); err != nil {
			return "", err
		}
		st, err := root.Lstat(name)
		if errors.Is(err, os.ErrNotExist) {
			continue // A deleted tracked file is absent before and after its commit.
		}
		if err != nil {
			return "", err
		}
		switch {
		case st.Mode()&os.ModeSymlink != 0:
			target, err := root.Readlink(name)
			if err != nil {
				return "", err
			}
			fmt.Fprintf(h, "%q link %q\n", name, target)
		case st.Mode().IsRegular():
			const limit = 256 << 20
			if st.Size() > limit-used {
				return "", errors.New("workspace contents exceed the 256 MiB fingerprint limit")
			}
			fmt.Fprintf(h, "%q file %t %d\n", name, st.Mode()&0o111 != 0, st.Size())
			f, err := root.Open(name)
			if err != nil {
				return "", err
			}
			opened, statErr := f.Stat()
			if statErr != nil || !os.SameFile(st, opened) {
				f.Close()
				return "", errors.New("workspace file changed while opening")
			}
			n, copyErr := io.Copy(h, io.LimitReader(f, limit-used+1))
			closeErr := f.Close()
			if copyErr != nil || closeErr != nil {
				return "", errors.Join(copyErr, closeErr)
			}
			if n != st.Size() || n > limit-used {
				return "", errors.New("workspace contents changed while fingerprinting")
			}
			used += n
		default:
			return "", fmt.Errorf("cannot fingerprint non-file %s (including submodules)", name)
		}
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}
