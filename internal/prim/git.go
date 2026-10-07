package prim

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/fclaude/nodwyn/internal/guard"
	"github.com/fclaude/nodwyn/internal/scheme"
)

// gitTimeout bounds one git-status or git-diff.
const gitTimeout = 30 * time.Second

// gitPrims are the read-only git tools, offered when the root is a git work
// tree: what changed, without a command to approve. They run git with fixed
// arguments only, and switch off everything in git that runs another
// program (external diff drivers, text conversion filters, the file system
// monitor, the pager), so reading a diff never executes repository or user
// configuration. A diff leaves out the files fs-read refuses; the status
// names every file, since a name is no secret and what changed must show:
// the evidence loop reads it to know whether the turn changed anything.
func gitPrims(o Options, pin guard.RepoPin) []*scheme.Prim {
	return []*scheme.Prim{
		{
			Name: "git-status",
			Meta: scheme.Meta{
				Doc:  "Show the git working tree status: the branch, and each changed, staged or untracked file (short format). Read-only; needs no approval.",
				Host: true, TaintSource: scheme.TagFile,
			},
			Handler: func(c *scheme.Call) (scheme.Value, error) {
				out, err := runGit(c.Ctx, o, "status", "--short", "--branch", "--untracked-files=normal", "--", ".")
				if err != nil {
					return scheme.Value{}, err
				}
				return scheme.Str(out), nil
			},
		},
		{
			Name: "git-diff",
			Meta: scheme.Meta{
				Doc: "Show the uncommitted changes as a unified diff: the working tree against the index, or with staged the index against the last commit; with against, the working tree (or with staged, the index) against that commit or branch instead, committed changes included. Untracked files are not in a diff: git-status lists them. Read-only; needs no approval.",
				Params: []scheme.Param{
					{Name: "path", Type: "string", Doc: "a file or directory to limit the diff to; \".\" for the whole repository", Role: "path", Optional: true},
					{Name: "staged", Type: "boolean", Doc: "true for the staged changes (git diff --cached)", Optional: true},
					{Name: "against", Type: "string", Doc: "a commit or branch to compare with instead of the last commit, such as the base a worktree started from: what changed since, committed or not", Optional: true},
				},
				Host: true, TaintSource: scheme.TagFile,
			},
			Handler: func(c *scheme.Call) (scheme.Value, error) {
				rel := "."
				if len(c.Args) > 0 {
					path, err := resolve(pin, c, 0)
					if err != nil {
						return scheme.Value{}, err
					}
					if rel, err = filepath.Rel(o.Root, path); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
						return scheme.Value{}, fmt.Errorf("git-diff: %s is not in the repository", c.Args[0].S)
					}
				}
				args := []string{"diff", "--no-ext-diff", "--no-textconv", "--no-color"}
				if len(c.Args) > 1 {
					if c.Args[1].Kind != scheme.KBool {
						return scheme.Value{}, errors.New("git-diff: staged must be true or false")
					}
					if c.Args[1].IsTrue() {
						args = append(args, "--cached")
					}
				}
				if len(c.Args) > 2 && !(c.Args[2].Kind == scheme.KString && c.Args[2].S == "") && !scheme.Equal(c.Args[2], scheme.False) {
					if c.Args[2].Kind != scheme.KString || !revision.MatchString(c.Args[2].S) {
						return scheme.Value{}, fmt.Errorf("git-diff: against must be a commit or branch name, got %s", scheme.Write(c.Args[2]))
					}
					// Never an option: git would read --output=FILE as one
					// and write the file.
					args = append(args, "--end-of-options", c.Args[2].S)
				}
				args = append(args, "--", filepath.ToSlash(rel))
				hidden, err := deniedChanges(c.Ctx, o, args)
				if err != nil {
					return scheme.Value{}, err
				}
				out, err := runGit(c.Ctx, o, append(args, hidden...)...)
				if err != nil {
					return scheme.Value{}, err
				}
				if out == "" {
					out = "(no changes)"
				}
				return scheme.Str(out), nil
			},
		},
		{
			Name: "git-head",
			Meta: scheme.Meta{
				Doc:  "The commit HEAD names now, as its full hash; #f before the first commit. A strategy measures a turn's change against the commit it started on, so a commit made in the turn moves nothing out of it.",
				Host: true, Hidden: true,
			},
			Handler: func(c *scheme.Call) (scheme.Value, error) {
				out, err := runGit(c.Ctx, o, "rev-parse", "--verify", "--quiet", "HEAD")
				if err != nil || strings.TrimSpace(out) == "" {
					return scheme.False, nil
				}
				return scheme.Str(strings.TrimSpace(out)), nil
			},
		},
	}
}

// revision is what git-diff takes to compare with: a hash, a branch or a
// tag, with git's ~, ^ and @{} suffixes; never anything that starts like an
// option.
var revision = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/~^@{}-]*$`)

// deniedChanges are pathspecs that leave out of the diff diffArgs asks
// for (its arguments from "diff" on) each changed file fs-read refuses, as
// the session's denylist judges it: the files are named by git and
// excluded by their exact names.
func deniedChanges(ctx context.Context, o Options, diffArgs []string) ([]string, error) {
	names, err := runGit(ctx, o, append([]string{diffArgs[0], "--name-only", "-z"}, diffArgs[1:]...)...)
	if err != nil {
		return nil, err
	}
	deny := denylist(o)
	var out []string
	for _, name := range strings.Split(names, "\x00") {
		if name != "" && deny.Denies(name) != "" {
			out = append(out, ":(top,exclude,literal)"+name)
		}
	}
	return out, nil
}

// Change is one file a session's worktree differs in from its base commit:
// Status is "A" (added or untracked), "M" or "D", and Diff is the unified
// diff of the file, or a note in parentheses when there is nothing to show
// as lines (a binary file, a link, a file too large to show).
type Change struct {
	Path   string `json:"path"`
	Status string `json:"status"`
	Diff   string `json:"diff"`
}

// The notes a Change's Diff holds in place of a diff.
const (
	noteTooLarge   = "(diff too large to show)"
	noteBinary     = "(binary file)"
	noteNotRegular = "(not a regular file: a link, folder or submodule)"
	noteUnreadable = "(could not read this file)"
	noteNoLines    = "(no lines changed)"
)

// ChangedSince lists what the working tree at o.Root changed from the base
// commit: committed, staged, unstaged and untracked files alike, as a
// reviewer sees a branch. Like git-diff it runs git with fixed arguments and
// no external programs, and it leaves out the files the session's denylist
// refuses to read. A file whose content is larger than perFile bytes is not
// read. The changes listed take at most total bytes as JSON, paths included,
// which is what a reply carries (JSON escapes a < or an & to six bytes, so
// the size of the text is no measure): the files after that are counted in
// omitted, and no git is run for them, so a branch of thousands of files
// neither takes long nor outgrows a reply. A file git cannot read gets a note
// of its own; only a failed listing or a cancelled ctx fails the whole call.
func ChangedSince(ctx context.Context, o Options, base string, perFile, total int) (changes []Change, omitted int, err error) {
	if base == "" || strings.HasPrefix(base, "-") {
		return nil, 0, errors.New("a base commit is required")
	}
	// Names only, so a long list is not cut where the output limit would
	// leave half a path.
	listing := o
	listing.MaxOutput = 16 << 20
	tracked, err := runGit(ctx, listing, "diff", "--name-status", "-z", "--no-renames", "--no-ext-diff", "--no-textconv", base, "--")
	if err != nil {
		return nil, 0, err
	}
	untracked, err := runGit(ctx, listing, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, 0, err
	}
	var named []Change
	fields := strings.Split(tracked, "\x00")
	for i := 0; i+1 < len(fields); i += 2 {
		named = append(named, Change{Path: fields[i+1], Status: fields[i][:1]})
	}
	for _, name := range strings.Split(untracked, "\x00") {
		if name != "" {
			named = append(named, Change{Path: name, Status: "A"})
		}
	}
	pin, err := os.OpenRoot(o.Root)
	if err != nil {
		return nil, 0, err
	}
	defer pin.Close()
	deny := denylist(o)
	used, full := 0, false
	for _, c := range named {
		if deny.Denies(c.Path) != "" {
			continue
		}
		if full {
			omitted++
			continue
		}
		c.Diff = changeDiff(ctx, o, pin, base, c, perFile)
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		// A Change always marshals; the comma is the one between entries.
		encoded, _ := json.Marshal(c)
		if used+len(encoded)+1 > total {
			full = true
			omitted++
			continue
		}
		used += len(encoded) + 1
		changes = append(changes, c)
	}
	return changes, omitted, nil
}

// changeDiff renders one change as a unified diff, or as a note when there
// are no lines to show. What is not a regular file now (a link, say) is not
// read, so the diff never follows it out of the root.
func changeDiff(ctx context.Context, o Options, pin *os.Root, base string, c Change, limit int) string {
	var before, after []byte
	if c.Status != "D" {
		info, err := pin.Lstat(c.Path)
		switch {
		case err == nil && !info.Mode().IsRegular():
			return noteNotRegular
		case err != nil && !errors.Is(err, os.ErrNotExist):
			return noteUnreadable
		case err == nil:
			if info.Size() > int64(limit) {
				return noteTooLarge
			}
			if after, err = pin.ReadFile(c.Path); err != nil {
				return noteUnreadable
			}
		}
	}
	if c.Status != "A" {
		// A submodule the base had has no object to read here.
		size, err := runGit(ctx, o, "cat-file", "-s", base+":"+c.Path)
		if err != nil {
			return noteUnreadable
		}
		if n, err := strconv.Atoi(strings.TrimSpace(size)); err != nil || n > limit {
			return noteTooLarge
		}
		out, err := runGit(ctx, o, "show", base+":"+c.Path)
		if err != nil {
			return noteUnreadable
		}
		before = []byte(out)
	}
	if bytes.IndexByte(before, 0) >= 0 || bytes.IndexByte(after, 0) >= 0 {
		return noteBinary
	}
	diff := Unified(c.Path, string(before), string(after), 3)
	// A change of mode alone, or an empty file, has headers and no lines.
	if !strings.Contains(diff, "\n@@ ") {
		if strings.Contains(diff, noteTooLarge) {
			return noteTooLarge
		}
		return noteNoLines
	}
	return diff
}

// runGit runs one fixed git command in the root and returns its output, or
// an error carrying git's own message.
func runGit(parent context.Context, o Options, args ...string) (string, error) {
	return runGitLimit(parent, o, o.MaxOutput, args...)
}

// runGitLimit is runGit with its own cap on what it returns: a file's whole
// content (cat-file) must not be cut at the size of a tool's answer.
func runGitLimit(parent context.Context, o Options, limit int, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(parent, gitTimeout)
	defer cancel()
	full := []string{"--no-pager", "--no-optional-locks",
		"-c", "core.fsmonitor=false", "-c", "core.pager=cat", "-c", "color.ui=false"}
	full = append(full, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Dir = o.Root
	cmd.Env = append(scrubEnv(o.EnvAllow), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &capped{&stdout, limit}
	cmd.Stderr = &capped{&stderr, 4096}
	err := cmd.Run()
	if parent.Err() != nil {
		return "", parent.Err()
	}
	if ctx.Err() != nil {
		return "", fmt.Errorf("git %s timed out after %s", args[0], gitTimeout)
	}
	if errors.Is(err, exec.ErrNotFound) {
		return "", fmt.Errorf("git %s: git is not installed", args[0])
	}
	if err != nil {
		return "", fmt.Errorf("git %s: %s", args[0], strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// isGitWorkTree reports whether root holds a .git directory or file (a
// worktree or submodule), where git is the undo and the git tools apply.
func isGitWorkTree(root string) bool {
	_, err := os.Stat(filepath.Join(root, ".git"))
	return err == nil
}
