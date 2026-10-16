package prim

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/fclaude/nodwyn/internal/detect/l1"
	"github.com/fclaude/nodwyn/internal/scheme"
)

// git-land lands a worktree session's work on its base branch: the step
// 34 of the 145 messages in the local session logs of 2026-09-30 to 10-05
// asked for ("commit and push to main"), which the model did through the shell
// in each session's own way. There, commit messages broke on shell quoting
// 22 times, signing failed on the first commit of every glm session (the
// shell's environment carries no agent socket), a rebase hung ten minutes
// on a passphrase prompt (20261004T063427Z-97214b45), models turned signing
// off or loaded the person's SSH key themselves (20261004T053047Z-1b4c67dc),
// and a branch was pushed where main was meant (20261004T035536Z-cd08f00d).
// It is ADR 34 §4's commit, rebase and push as one step with fixed
// arguments (ADR 34, amended 2026-10-05):
//
//  1. commit what changed, the files fs-read refuses left out, with the
//     message as given, signed as the person's git configuration says, with
//     the person's agent;
//  2. rebase the session's own commits onto the newest base (its upstream,
//     fetched first, when it has one), never anyone else's; a conflict
//     stops it with the files named, for the model to resolve and call
//     git-land again;
//  3. refuse a change whose outgoing diff adds credential-shaped text;
//  4. push it to the base branch's upstream by fast-forward only, never
//     forced;
//  5. move the local base branch too when that is safe: its ref when no
//     checkout has it, a fast-forward merge in a clean checkout that has
//     it, and nothing, said, in one with changes of its own.
//
// It asks first, as any irreversible call does; nothing in it can be
// pointed elsewhere by the model, which gives only the message.

// landTimeout bounds each git command a landing runs; a push or a fetch
// talks to a server.
const landTimeout = 2 * time.Minute

// landEnvAllow is what git's environment for a landing adds to the
// session's: the session's, plus what signing and an SSH remote need from the
// person's own session (their agents, and the ssh command they chose, such as
// one naming a key), with every prompt off, so a passphrase prompt fails at
// once instead of hanging.
var landEnvAllow = []string{"SSH_AUTH_SOCK", "GNUPGHOME", "GPG_AGENT_INFO", "SSH_AGENT_PID", "GIT_SSH_COMMAND"}

// landEnv is git's environment for a landing.
func landEnv(o Options) []string {
	env := append(scrubEnv(append(append([]string(nil), o.EnvAllow...), landEnvAllow...)),
		// Names from status are names, not patterns: a file called * would
		// otherwise match every file, the refused ones included.
		"GIT_LITERAL_PATHSPECS=1",
		// SSH_ASKPASS_REQUIRE makes ssh use the askpass program, which
		// answers nothing, even when a terminal is there to prompt on.
		"GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "GIT_EDITOR=true", "GIT_SEQUENCE_EDITOR=true", "GIT_ASKPASS=true", "SSH_ASKPASS=true", "SSH_ASKPASS_REQUIRE=force")
	// Decided from the child's own environment: the person's command, when
	// they have one and it came through, is theirs to choose.
	if !slices.ContainsFunc(env, func(kv string) bool { return strings.HasPrefix(kv, "GIT_SSH_COMMAND=") }) {
		env = append(env, "GIT_SSH_COMMAND=ssh -o BatchMode=yes")
	}
	return env
}

func gitLand(o Options) *scheme.Prim {
	return &scheme.Prim{
		Name: "git-land",
		Meta: scheme.Meta{
			Doc: "Land this session's work on its base branch, in one step: commit every change with message (files fs-read refuses are left out), rebase your commits onto the newest base, push them to it by fast-forward, and move the local base branch when that is safe. Call it when the request asks for the work to land (\"commit and push\", \"push to main\"). On a rebase conflict it stops and names the files: resolve them with fs-edit and call it again. Write the message as the repository's history does: a subject line saying what is now true, a blank line, and why.",
			Params: []scheme.Param{
				{Name: "message", Type: "string", Doc: "the commit message for the changes not yet committed; ignored when everything is committed"},
			},
			Host:          true,
			Irreversible:  true,
			ConfirmReason: "git-land commits, rebases and pushes this session's work to its base branch",
		},
		Handler: func(c *scheme.Call) (scheme.Value, error) {
			msg := ""
			if len(c.Args) > 0 && c.Args[0].Kind == scheme.KString {
				msg = strings.TrimSpace(c.Args[0].S)
			}
			text, err := land(c.Ctx, o, msg)
			if err != nil {
				return scheme.Value{}, err
			}
			return scheme.Str(text), nil
		},
	}
}

// lander runs git for a landing in a directory.
type lander struct {
	ctx context.Context
	o   Options
	env []string
}

func (l lander) git(dir string, args ...string) (string, error) {
	out, err := l.run(dir, true, args...)
	return strings.TrimSpace(out), err
}

// records is git's standard output as it gave it, for NUL-separated records
// whose leading spaces mean something (status lines). A warning on standard
// error would read as a record, so it is left out unless git fails.
func (l lander) records(dir string, args ...string) (string, error) {
	return l.run(dir, false, args...)
}

// run runs git in dir; combined puts standard error in the output too.
func (l lander) run(dir string, combined bool, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(l.ctx, landTimeout)
	defer cancel()
	cmd := gitCommand(ctx, dir, l.env, []string{"core.editor=true", "sequence.editor=true"}, args)
	var out []byte
	var err error
	if combined {
		out, err = cmd.CombinedOutput()
	} else {
		var stderr strings.Builder
		cmd.Stderr = &stderr
		if out, err = cmd.Output(); err != nil {
			out = append([]byte(stderr.String()), out...)
		}
	}
	if ctx.Err() != nil && l.ctx.Err() == nil {
		return string(out), fmt.Errorf("git %s gave no answer in %s: a prompt nobody can answer, or a server that does not reply", args[0], landTimeout)
	}
	if err != nil {
		return string(out), fmt.Errorf("git %s: %s", args[0], strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func land(ctx context.Context, o Options, message string) (string, error) {
	w := o.Worktree
	if w == nil {
		return "", errors.New("this session has no worktree of its own; its work is in the folder itself")
	}
	l := lander{ctx: ctx, o: o, env: landEnv(o)}
	root := o.Root
	if branch, err := l.git(root, "rev-parse", "--abbrev-ref", "HEAD"); err != nil || branch != w.Branch {
		if inRebase(l, root) {
			return l.continueRebase(w)
		}
		return "", fmt.Errorf("HEAD is %q, not this session's branch %s", branch, w.Branch)
	}
	if hooks, err := l.git(root, "config", "--get", "core.hooksPath"); err == nil && hooks != "" {
		abs := hooks
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(root, hooks)
		}
		if rel, err := filepath.Rel(root, abs); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("core.hooksPath is %s, inside this worktree, where the model could have written the hooks a commit runs: land it from a terminal", hooks)
		}
	}
	var said []string
	// 1. Commit what changed.
	committed, err := l.commit(message)
	if err != nil {
		return "", err
	}
	if committed != "" {
		said = append(said, committed)
	}
	// 2. Rebase the session's own commits onto the newest base.
	onto, remote, ref, err := l.base(w)
	if err != nil {
		return "", err
	}
	from := w.BaseCommit
	if _, err := l.git(root, "merge-base", "--is-ancestor", w.BaseCommit, onto); err == nil {
		// The base commit is on the base: what the session made is what
		// HEAD has that the base does not.
		if from, err = l.git(root, "merge-base", "HEAD", onto); err != nil {
			return "", err
		}
	}
	ahead, _ := l.git(root, "rev-list", "--count", onto+"..HEAD")
	if ahead == "0" {
		return strings.Join(append(said, "nothing to land: "+w.Branch+" has no commit "+onto+" lacks"), "\n"), nil
	}
	if out, err := l.git(root, "rebase", "--onto", onto, from); err != nil {
		if inRebase(l, root) {
			return "", l.conflict(out)
		}
		return "", fmt.Errorf("rebasing onto %s: %w", onto, err)
	}
	return l.publish(w, onto, remote, ref, said)
}

// commit commits every change but the files fs-read refuses, and says so;
// "" when there was nothing to commit.
func (l lander) commit(message string) (string, error) {
	root := l.o.Root
	// --no-renames: a rename is its two paths, each a record of its own, and
	// each judged on its own.
	status, err := l.records(root, "status", "--porcelain=v1", "-z", "--no-renames", "--untracked-files=all")
	if err != nil {
		return "", err
	}
	deny := denylist(l.o)
	var add, left []string
	for _, rec := range strings.Split(status, "\x00") {
		if len(rec) < 4 {
			continue
		}
		name := rec[3:]
		if deny.Denies(name) != "" {
			left = append(left, name)
			continue
		}
		add = append(add, name)
	}
	if len(add) == 0 {
		return "", nil
	}
	if message == "" {
		return "", errors.New("there are changes to commit: give the commit message")
	}
	// Everything is staged, then the refused files are taken out: the commit
	// takes the whole index, and one may be in it already (a git add -A
	// through the shell). Naming each path to add would fail on one already
	// staged as deleted or renamed. Nothing is committed if the index still
	// holds a refused file.
	if _, err := l.git(root, "add", "-A"); err != nil {
		return "", err
	}
	if len(left) > 0 {
		if _, err := l.git(root, append([]string{"reset", "-q", "--"}, left...)...); err != nil {
			return "", err
		}
	}
	staged, err := l.records(root, "diff", "--cached", "--name-only", "-z", "--no-renames")
	if err != nil {
		return "", err
	}
	for _, name := range strings.Split(staged, "\x00") {
		if name != "" && deny.Denies(name) != "" {
			return "", fmt.Errorf("%s is staged and fs-read refuses it, so nothing was committed: unstage it (git reset -- %s), or land the work from a terminal if it belongs", name, name)
		}
	}
	if out, err := l.git(root, "commit", "-q", "-m", message); err != nil {
		if strings.Contains(out, "sign") || strings.Contains(out, "gpg") || strings.Contains(out, "agent") {
			return "", fmt.Errorf("the commit could not be signed as this repository's git configuration asks (%s); nothing was committed. The person's agent may be locked or not reachable from nodwyn: ask them to unlock it, or to land the work from a terminal", strings.TrimSpace(lastLine(out)))
		}
		return "", err
	}
	head, _ := l.git(root, "log", "-1", "--format=%h %s")
	said := "committed " + head
	if len(left) > 0 {
		said += "; left out, as files fs-read refuses: " + strings.Join(left, ", ")
	}
	return said, nil
}

// base is what the session's work lands on: the base branch's upstream,
// fetched first, when it has one (its remote and branch), else the local
// base branch.
func (l lander) base(w *Worktree) (onto, remote, ref string, err error) {
	root := l.o.Root
	remote, _ = l.git(root, "config", "--get", "branch."+w.Base+".remote")
	merge, _ := l.git(root, "config", "--get", "branch."+w.Base+".merge")
	if remote == "" || remote == "." || !strings.HasPrefix(merge, "refs/heads/") {
		return "refs/heads/" + w.Base, "", "", nil
	}
	ref = strings.TrimPrefix(merge, "refs/heads/")
	if _, err := l.git(root, "fetch", "-q", remote, ref); err != nil {
		return "", "", "", fmt.Errorf("fetching %s from %s: %w", ref, remote, err)
	}
	return "refs/remotes/" + remote + "/" + ref, remote, ref, nil
}

// publish pushes the rebased branch to the base's upstream by fast-forward,
// after checking what leaves, and moves the local base branch when safe.
func (l lander) publish(w *Worktree, onto, remote, ref string, said []string) (string, error) {
	root := l.o.Root
	head, err := l.git(root, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	count, _ := l.git(root, "rev-list", "--count", onto+"..HEAD")
	// Only what the change adds can leak: a removed line is the person
	// taking a credential out, and context is text they did not write.
	diff, err := l.git(root, "diff", "--no-ext-diff", "--no-textconv", "-U0", onto, "HEAD")
	if err != nil {
		return "", err
	}
	for _, f := range l1.New().Scan(addedText(diff)) {
		if f.Class != l1.ClassUnknownEntropy {
			return "", fmt.Errorf("the change holds a %s-shaped text, so nothing was pushed; take it out, or land it from a terminal if it belongs", f.Class)
		}
	}
	if remote != "" {
		if _, err := l.git(root, "push", "-q", remote, "HEAD:refs/heads/"+ref); err != nil {
			return "", fmt.Errorf("pushing to %s %s (fast-forward only): %w. If %s moved meanwhile, call git-land again to rebase onto it", remote, ref, err, ref)
		}
		said = append(said, fmt.Sprintf("pushed %s commit(s) to %s %s: it is now %s", count, remote, ref, short(head)))
	}
	said = append(said, l.moveLocal(w, head))
	return strings.Join(said, "\n"), nil
}

// addedText is the lines a unified diff adds, joined by newlines, so that the
// lines of one block stay together for the scanner. Lines before a file's
// first hunk are its header, not text.
func addedText(diff string) string {
	var added []string
	inHunk := false
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			inHunk = false
		case strings.HasPrefix(line, "@@"):
			inHunk = true
		case inHunk && strings.HasPrefix(line, "+"):
			added = append(added, line[1:])
		}
	}
	return strings.Join(added, "\n")
}

// moveLocal moves the local base branch to head when that is a fast
// forward and safe, and says what it did.
func (l lander) moveLocal(w *Worktree, head string) string {
	root := l.o.Root
	ref := "refs/heads/" + w.Base
	old, err := l.git(root, "rev-parse", "--verify", "--quiet", ref)
	if err != nil {
		return "the local " + w.Base + " does not exist; nothing moved"
	}
	if old == head {
		return "the local " + w.Base + " is already there"
	}
	if _, err := l.git(root, "merge-base", "--is-ancestor", old, head); err != nil {
		return "the local " + w.Base + " has commits the landed work lacks, so it was left alone: pull it"
	}
	list, _ := l.git(root, "worktree", "list", "--porcelain")
	checkout := ""
	for _, block := range strings.Split(list, "\n\n") {
		path := ""
		for _, line := range strings.Split(block, "\n") {
			if p, ok := strings.CutPrefix(line, "worktree "); ok {
				path = p
			}
			if line == "branch "+ref {
				checkout = path
			}
		}
	}
	if checkout == "" {
		if _, err := l.git(root, "update-ref", ref, head, old); err != nil {
			return "the local " + w.Base + " could not be moved: " + err.Error()
		}
		return "the local " + w.Base + " moved to " + short(head)
	}
	if changes, _ := l.git(checkout, "status", "--porcelain", "--untracked-files=no"); changes != "" {
		return "the checkout at " + checkout + " has changes of its own, so its " + w.Base + " was left alone: pull it when they are done"
	}
	if _, err := l.git(checkout, "merge", "-q", "--ff-only", head); err != nil {
		return "the checkout at " + checkout + " could not be fast-forwarded: " + err.Error()
	}
	return "the checkout at " + checkout + " fast-forwarded to " + short(head)
}

// inRebase reports a rebase in progress in the worktree.
func inRebase(l lander, root string) bool {
	for _, d := range []string{"rebase-merge", "rebase-apply"} {
		if p, err := l.git(root, "rev-parse", "--git-path", d); err == nil {
			if !filepath.IsAbs(p) {
				p = filepath.Join(root, p)
			}
			if st, err := os.Stat(p); err == nil && st.IsDir() {
				return true
			}
		}
	}
	return false
}

// conflict says which files a rebase stopped on.
func (l lander) conflict(out string) error {
	files, _ := l.git(l.o.Root, "diff", "--name-only", "--diff-filter=U")
	return fmt.Errorf("rebasing onto the newest base stopped on a conflict in: %s. Resolve the conflict markers in each with fs-edit (read them first), then call git-land again to go on; nothing was pushed", strings.Join(strings.Fields(files), ", "))
}

// continueRebase goes on with a rebase git-land stopped, once the model has
// resolved its conflicts, and lands the result.
func (l lander) continueRebase(w *Worktree) (string, error) {
	root := l.o.Root
	files, _ := l.git(root, "diff", "--name-only", "--diff-filter=U")
	for _, f := range strings.Fields(files) {
		raw, err := os.ReadFile(filepath.Join(root, f))
		if err == nil && (strings.Contains(string(raw), "<<<<<<< ") || strings.Contains(string(raw), ">>>>>>> ")) {
			return "", fmt.Errorf("%s still has conflict markers: resolve them with fs-edit, then call git-land again", f)
		}
		if _, err := l.git(root, "add", "--", f); err != nil {
			return "", err
		}
	}
	if out, err := l.git(root, "rebase", "--continue"); err != nil {
		if inRebase(l, root) {
			return "", l.conflict(out)
		}
		return "", fmt.Errorf("going on with the rebase: %w", err)
	}
	onto, remote, ref, err := l.base(w)
	if err != nil {
		return "", err
	}
	return l.publish(w, onto, remote, ref, []string{"rebase finished"})
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}

func short(hash string) string {
	if len(hash) > 7 {
		return hash[:7]
	}
	return hash
}
