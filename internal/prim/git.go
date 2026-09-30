package prim

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
// configuration. Credential-looking files are left out by pathspec, as
// fs-read refuses them.
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
				Doc: "Show the uncommitted changes as a unified diff: the working tree against the index, or with staged the index against the last commit. Untracked files are not in a diff: git-status lists them. Read-only; needs no approval.",
				Params: []scheme.Param{
					{Name: "path", Type: "string", Doc: "a file or directory to limit the diff to; \".\" for the whole repository", Role: "path", Optional: true},
					{Name: "staged", Type: "boolean", Doc: "true for the staged changes (git diff --cached)", Optional: true},
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
				out, err := runGit(c.Ctx, o, append(args, "--", filepath.ToSlash(rel))...)
				if err != nil {
					return scheme.Value{}, err
				}
				if out == "" {
					out = "(no changes)"
				}
				return scheme.Str(out), nil
			},
		},
	}
}

// runGit runs one fixed git command in the root and returns its output, or
// an error carrying git's own message.
func runGit(parent context.Context, o Options, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(parent, gitTimeout)
	defer cancel()
	full := []string{"--no-pager", "--no-optional-locks",
		"-c", "core.fsmonitor=false", "-c", "core.pager=cat", "-c", "color.ui=false"}
	full = append(full, args...)
	// Leave credential-looking files out, as fs-read does.
	for _, glob := range guard.DefaultDenylist().Globs {
		full = append(full, ":(exclude,glob)**/"+glob)
	}
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Dir = o.Root
	cmd.Env = append(scrubEnv(o.EnvAllow), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &capped{&stdout, o.MaxOutput}
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
