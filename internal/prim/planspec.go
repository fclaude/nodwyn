package prim

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/fclaude/nodwyn/internal/scheme"
)

// A plan can be written as Scheme data, one form per step (planspec.scm):
// what the plan names, the files, the tests and the commands that run them,
// and the names the change must produce, are then fields the harness reads
// instead of words in a sentence. These primitives are its host side: read
// the data without running it, take the names a request gives, and set a
// plan's names and files against the change as git shows it (ADR 32,
// amended 2026-10-13).

// readData builds (read-data text): every datum in text, unevaluated.
func readData() *scheme.Prim {
	return &scheme.Prim{
		Name: "read-data",
		Meta: scheme.Meta{
			Doc:    "Read every datum in text as data, without evaluating it: a list of the forms, strings, symbols and numbers it holds. An error names where text stops being readable.",
			Params: []scheme.Param{{Name: "text", Type: "string", Doc: "the text to read"}},
			Hidden: true,
		},
		Handler: func(c *scheme.Call) (scheme.Value, error) {
			text, err := c.String(0)
			if err != nil {
				return scheme.Value{}, err
			}
			vs, err := scheme.Read(text)
			if err != nil {
				return scheme.Value{}, fmt.Errorf("read-data: %w", err)
			}
			return scheme.List(vs...), nil
		},
	}
}

var (
	requestNameRE = regexp.MustCompile("`([^`\\n]{2,60})`|\\b([A-Za-z][A-Za-z0-9]*(?:[-_.][A-Za-z0-9]+)+|[a-z]+[A-Z][A-Za-z0-9]*|[A-Z][a-z0-9]+[A-Z][A-Za-z0-9]*)\\b")
	issueRefRE    = regexp.MustCompile(`#\d+$`)
	linkRE        = regexp.MustCompile(`\b[A-Za-z][A-Za-z0-9+.-]*://\S+`)
	// markerRE is a word in capitals, with an extension or not: a marker or
	// a file every task names (NODWYN_RUN_COMPLETE, AGENTS.md), not a name the
	// change produces. brandRE is a capitalised word with a capital inside
	// and no separator (ChatGPT, OpenAI): a product's name more often than the
	// code's. On commit-bench both made the planner write its plan again
	// for names that did not matter (ADR 32, amended 2026-10-13).
	markerRE  = regexp.MustCompile(`^[A-Z][A-Z0-9_]*(\.[A-Za-z0-9]+)?$`)
	brandRE   = regexp.MustCompile(`^[A-Z][A-Za-z0-9]*$`)
	versionRE = regexp.MustCompile(`^v?\d+([._-]\d+)*$`)
)

// requestNames is the names a request gives that read as code: spans in
// backquotes without a space, and words with a hyphen, an underscore or a
// dot inside, or a small letter first and a capital after it (session-id,
// cache_key, response.output_item.done, readResponses). Not a link, an issue
// number, a version, a word in capitals (markerRE) or a capitalised one
// without a separator (brandRE).
func requestNames(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range requestNameRE.FindAllStringSubmatch(linkRE.ReplaceAllString(text, " "), -1) {
		s := strings.TrimSpace(m[1])
		if s == "" {
			s = m[2]
		}
		if s == "" || strings.ContainsAny(s, " \t") || strings.Contains(s, "://") || strings.HasPrefix(s, "www.") ||
			issueRefRE.MatchString(s) || versionRE.MatchString(s) || s == "e.g" || s == "i.e" ||
			markerRE.MatchString(s) || brandRE.MatchString(s) {
			continue
		}
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func requestNamesPrim() *scheme.Prim {
	return &scheme.Prim{
		Name: "request-names",
		Meta: scheme.Meta{
			Doc:    "The names a request gives, in order, each once: spans in backquotes without a space, and words that read as names (with a hyphen, an underscore or a dot inside, or a capital after the first letter: session-id, cache_key, readResponses). Not links, issue numbers or versions.",
			Params: []scheme.Param{{Name: "text", Type: "string", Doc: "the request"}},
			Hidden: true,
		},
		Handler: func(c *scheme.Call) (scheme.Value, error) {
			text, err := c.String(0)
			if err != nil {
				return scheme.Value{}, err
			}
			names := requestNames(text)
			out := make([]scheme.Value, len(names))
			for i, n := range names {
				out[i] = scheme.Str(n)
			}
			return scheme.List(out...), nil
		},
	}
}

// hasName is whether name is in text as a whole name, in any case: the
// characters around it are not letters, digits, an underscore or a hyphen,
// so session-id is not found in X-Session-Id.
func hasName(text, name string) bool {
	if name == "" {
		return false
	}
	lt, ln := strings.ToLower(text), strings.ToLower(name)
	part := func(b byte) bool {
		return b == '_' || b == '-' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z'
	}
	for from := 0; ; {
		i := strings.Index(lt[from:], ln)
		if i < 0 {
			return false
		}
		i += from
		j := i + len(ln)
		if (i == 0 || !part(lt[i-1])) && (j == len(lt) || !part(lt[j])) {
			return true
		}
		from = i + 1
	}
}

// changeText is what the uncommitted change adds: the added lines of the
// tracked files' diff against HEAD, and the whole of each new file, each
// cut at withoutMaxFile; and the paths it changes.
func changeText(ctx context.Context, o Options) (string, map[string]bool, error) {
	k := &Keeper{o: o}
	_, paths, err := k.look(ctx)
	if err != nil {
		return "", nil, err
	}
	changed := map[string]bool{}
	for _, p := range paths {
		changed[filepath.ToSlash(filepath.Clean(p))] = true
	}
	diff, err := runGitLimit(ctx, o, 16<<20, "diff", "HEAD", "--no-color", "--no-ext-diff", "-U0")
	if err != nil {
		return "", nil, err
	}
	var b strings.Builder
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			b.WriteString(line[1:])
			b.WriteByte('\n')
		}
	}
	tracked, err := k.headFiles(ctx)
	if err != nil {
		return "", nil, err
	}
	for _, p := range paths {
		if _, inHead := tracked[p]; inHead {
			continue
		}
		abs := filepath.Join(o.Root, filepath.FromSlash(p))
		if info, err := os.Lstat(abs); err != nil || !info.Mode().IsRegular() || info.Size() > withoutMaxFile {
			continue
		}
		data, err := os.ReadFile(abs)
		if err == nil {
			b.Write(data)
			b.WriteByte('\n')
		}
	}
	return b.String(), changed, nil
}

// planEvidence builds (plan-evidence names paths): the names the change does
// not contain and the paths it does not change.
func planEvidence(o Options) *scheme.Prim {
	return &scheme.Prim{
		Name: "plan-evidence",
		Meta: scheme.Meta{
			Doc:    "Set a plan against the uncommitted change as git shows it: an alist of missing, the names the change does not add (a name is found whole and in any case in the added lines and new files: session-id is not found in X-Session-Id), and unchanged, the paths it does not change. Registered in a git work tree.",
			Params: []scheme.Param{{Name: "names", Type: "list", Doc: "names the change must add"}, {Name: "paths", Type: "list", Doc: "repository paths the change must touch"}},
			Host:   true,
			Hidden: true,
		},
		Handler: func(c *scheme.Call) (scheme.Value, error) {
			strs := func(i int) ([]string, error) {
				items, err := c.List(i)
				if err != nil {
					return nil, err
				}
				out := make([]string, 0, len(items))
				for _, it := range items {
					if it.Kind != scheme.KString {
						return nil, errors.New("plan-evidence takes lists of strings")
					}
					out = append(out, it.S)
				}
				return out, nil
			}
			names, err := strs(0)
			if err != nil {
				return scheme.Value{}, err
			}
			paths, err := strs(1)
			if err != nil {
				return scheme.Value{}, err
			}
			text, changed, err := changeText(c.Ctx, o)
			if err != nil {
				return scheme.Value{}, fmt.Errorf("plan-evidence: %w", err)
			}
			var missing, unchanged []scheme.Value
			for _, n := range names {
				// A phrase is a quote, and a name the unchanged code has is
				// no name the change must produce: asked for them, a model
				// on commit-bench wrote an old error message and a setting
				// the code already had into its ADR (5040f9fe, 2026-10-13).
				if strings.ContainsAny(n, " \t\n") || headHasName(c.Ctx, o, n) {
					continue
				}
				if !hasName(text, n) {
					missing = append(missing, scheme.Str(n))
				}
			}
			for _, p := range paths {
				clean := filepath.ToSlash(filepath.Clean(strings.TrimPrefix(p, "./")))
				if !changed[clean] && !changedUnder(changed, clean) {
					unchanged = append(unchanged, scheme.Str(p))
				}
			}
			return scheme.List(
				scheme.Cons(scheme.Str("missing"), scheme.List(missing...)),
				scheme.Cons(scheme.Str("unchanged"), scheme.List(unchanged...)),
			), nil
		},
	}
}

// changedUnder is whether the change touches a file under dir: a plan's
// step may name a directory (docs/adr/, its planner not knowing the file),
// and an edit to a file in it is that step's change (commit-bench, dsl8
// 107251d0, 2026-10-13).
func changedUnder(changed map[string]bool, dir string) bool {
	if dir == "." || dir == "" {
		return false
	}
	for p := range changed {
		if strings.HasPrefix(p, dir+"/") {
			return true
		}
	}
	return false
}

// headHasName is whether a file of HEAD holds name whole (hasName): git
// grep finds the files with it in any case, and up to headNameFiles of them
// are read whole.
func headHasName(ctx context.Context, o Options, name string) bool {
	out, err := runGitLimit(ctx, o, 1<<20, "grep", "-I", "-i", "-F", "-l", "-e", name, "HEAD", "--")
	if err != nil {
		return false
	}
	files := strings.Fields(out)
	if len(files) > headNameFiles {
		files = files[:headNameFiles]
	}
	for _, f := range files {
		blob, err := runGitLimit(ctx, o, withoutMaxFile, "show", f)
		if err == nil && hasName(blob, name) {
			return true
		}
	}
	return false
}

const headNameFiles = 40

// testBeforeBudget is how long (test-before ...) may run a test.
var testBeforeBudget = 3 * time.Minute

// testBefore builds (test-before path cmd args): run a test on the code as it
// was before the change. A copy of HEAD goes to plan-base in the scratch
// directory, the working tree's version of the test file at path is put in
// it, and the command runs there. A test the plan says fails today and that
// passes there does not show the change: the review of commit-bench's
// patches ran each patch's own tests on the unchanged code by hand to find
// those (3 of 22 nodwyn tests, 2026-10-12); this is the same check, made by
// the harness at no cost in tokens.
func testBefore(o Options) *scheme.Prim {
	return &scheme.Prim{
		Name: "test-before",
		Meta: scheme.Meta{
			Doc: "Run a test on the code as it was before the change: a copy of HEAD in the scratch directory (plan-base) with the working tree's version of the file at path put in, the command run there within " + testBeforeBudget.String() + ". Returns its exit status, or fails when it could not run. Hidden; registered in a git work tree; its command is judged by the guard as a command's is.",
			Params: []scheme.Param{
				{Name: "path", Type: "string", Doc: "the test file, relative to the repository root"},
				{Name: "cmd", Type: "string", Doc: "the program", Role: "argv"},
				{Name: "args", Type: "list", Doc: "its arguments", Role: "argv"},
			},
			Host:   true,
			Hidden: true,
		},
		Handler: func(c *scheme.Call) (scheme.Value, error) {
			rel, err := c.String(0)
			if err != nil {
				return scheme.Value{}, err
			}
			name, err := c.String(1)
			if err != nil {
				return scheme.Value{}, err
			}
			items, err := c.List(2)
			if err != nil {
				return scheme.Value{}, err
			}
			args := make([]string, len(items))
			for i, it := range items {
				if it.Kind != scheme.KString {
					return scheme.Value{}, errors.New("args must be a list of strings")
				}
				args[i] = it.S
			}
			status, err := runTestBefore(c.Ctx, o, rel, name, args)
			if err != nil {
				return scheme.Value{}, fmt.Errorf("test-before: %w", err)
			}
			return scheme.Int(int64(status)), nil
		},
	}
}

func runTestBefore(ctx context.Context, o Options, rel, name string, args []string) (int, error) {
	if o.Scratch == "" || !isGitWorkTree(o.Root) {
		return 0, errors.New("not a git work tree with a scratch directory")
	}
	clean := filepath.Clean(filepath.FromSlash(strings.TrimPrefix(rel, "./")))
	if filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") {
		return 0, errors.New("the test file is not a repository path")
	}
	src := filepath.Join(o.Root, clean)
	data, err := os.ReadFile(src)
	if err != nil {
		return 0, err
	}
	base := filepath.Join(o.Scratch, "plan-base")
	if err := os.RemoveAll(base); err != nil {
		return 0, err
	}
	if err := os.MkdirAll(base, 0o755); err != nil {
		return 0, err
	}
	actx, cancel := context.WithTimeout(ctx, testBeforeBudget)
	defer cancel()
	archive := exec.CommandContext(actx, "git", "archive", "--format=tar", "HEAD")
	archive.Dir = o.Root
	archive.Env = scrubEnv(o.EnvAllow)
	untar := exec.CommandContext(actx, "tar", "-x", "-C", base)
	pipe, err := archive.StdoutPipe()
	if err != nil {
		return 0, err
	}
	untar.Stdin = pipe
	if err := untar.Start(); err != nil {
		return 0, err
	}
	if err := archive.Run(); err != nil {
		_ = untar.Wait()
		return 0, fmt.Errorf("git archive: %w", err)
	}
	if err := untar.Wait(); err != nil {
		return 0, fmt.Errorf("tar: %w", err)
	}
	dst := filepath.Join(base, clean)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return 0, err
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		return 0, err
	}
	run := o
	run.Root = base
	r, err := runCheck(ctx, run, name, args, time.Now().Add(testBeforeBudget))
	if err != nil {
		return 0, err
	}
	return r.status, nil
}

// failureLineRE is a line of a command's output that says something failed:
// a test or a step that failed, an error, a panic, a missing file or
// program. On commit-bench the plan's check, make validate, failed in every
// attempt of every build on a program the environment lacks (staticcheck:
// No such file or directory), and the model spent up to 19 calls of an
// attempt finding that out (2026-10-13).
var failureLineRE = regexp.MustCompile(`(?i)\b(fail|fails|failed|failing|failure|error|errors|panic|fatal)\b|no such file|not found|cannot |undefined: `)

// failureLines is the lines of text that say something failed, each with
// its noise masked (checkNoise) and its spaces made single, once each, in
// order: what two runs of a check are compared by.
func failureLines(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, line := range strings.FieldsFunc(text, func(r rune) bool { return r == '\n' || r == '\r' }) {
		if !failureLineRE.MatchString(line) {
			continue
		}
		for _, n := range checkNoise {
			line = n.re.ReplaceAllString(line, n.with)
		}
		line = strings.Join(strings.Fields(line), " ")
		if line != "" && !seen[line] {
			seen[line] = true
			out = append(out, line)
		}
	}
	return out
}

func failureLinesPrim() *scheme.Prim {
	return &scheme.Prim{
		Name: "failure-lines",
		Meta: scheme.Meta{
			Doc:    "The lines of a command's output that say something failed (a failed test or step, an error, a panic, a missing file or program), each with durations, clock times, addresses and long numbers masked and its spaces made single, once each, in order.",
			Params: []scheme.Param{{Name: "text", Type: "string", Doc: "the output"}},
			Hidden: true,
		},
		Handler: func(c *scheme.Call) (scheme.Value, error) {
			text, err := c.String(0)
			if err != nil {
				return scheme.Value{}, err
			}
			lines := failureLines(text)
			out := make([]scheme.Value, len(lines))
			for i, l := range lines {
				out[i] = scheme.Str(l)
			}
			return scheme.List(out...), nil
		},
	}
}

// checkBeforeBudget is how long (check-before ...) may run the plan's check,
// and checkBeforeShown how many of its failing lines it gives as printed.
var checkBeforeBudget = 10 * time.Minute

const checkBeforeShown = 8

// checkBefore builds (check-before cmd args): run the plan's check on the
// code as it was, so a failure the check had before the change is told
// apart from one the change made.
func checkBefore(o Options) *scheme.Prim {
	return &scheme.Prim{
		Name: "check-before",
		Meta: scheme.Meta{
			Doc: "Run a check on the code as it was before the change: a clone of HEAD in the scratch directory (plan-check-base), the command run there within " + checkBeforeBudget.String() + ". Returns an alist of status, its exit status; lines, the lines of its output that say something failed, as failure-lines gives them; and shown, the first of those lines as printed. Fails when it could not run. Hidden; registered in a git work tree; its command is judged by the guard as a command's is.",
			Params: []scheme.Param{
				{Name: "cmd", Type: "string", Doc: "the program", Role: "argv"},
				{Name: "args", Type: "list", Doc: "its arguments", Role: "argv"},
			},
			Host:   true,
			Hidden: true,
		},
		Handler: func(c *scheme.Call) (scheme.Value, error) {
			name, err := c.String(0)
			if err != nil {
				return scheme.Value{}, err
			}
			items, err := c.List(1)
			if err != nil {
				return scheme.Value{}, err
			}
			args := make([]string, len(items))
			for i, it := range items {
				if it.Kind != scheme.KString {
					return scheme.Value{}, errors.New("args must be a list of strings")
				}
				args[i] = it.S
			}
			status, output, err := runCheckBefore(c.Ctx, o, name, args)
			if err != nil {
				return scheme.Value{}, fmt.Errorf("check-before: %w", err)
			}
			lines := failureLines(output)
			var norm, shown []scheme.Value
			for _, l := range lines {
				norm = append(norm, scheme.Str(l))
			}
			for _, l := range strings.FieldsFunc(output, func(r rune) bool { return r == '\n' || r == '\r' }) {
				if len(shown) == checkBeforeShown {
					break
				}
				if failureLineRE.MatchString(l) {
					if len(l) > 200 {
						l = l[:200] + "..."
					}
					shown = append(shown, scheme.Str(strings.TrimSpace(l)))
				}
			}
			return scheme.List(
				scheme.Cons(scheme.Str("status"), scheme.Int(int64(status))),
				scheme.Cons(scheme.Str("lines"), scheme.List(norm...)),
				scheme.Cons(scheme.Str("shown"), scheme.List(shown...)),
			), nil
		},
	}
}

// checkBeforeOutput keeps what a command prints, up to its last
// checkBeforeKeep bytes.
type checkBeforeOutput struct{ b []byte }

const checkBeforeKeep = 4 << 20

func (w *checkBeforeOutput) Write(p []byte) (int, error) {
	w.b = append(w.b, p...)
	if len(w.b) > 2*checkBeforeKeep {
		w.b = append([]byte(nil), w.b[len(w.b)-checkBeforeKeep:]...)
	}
	return len(p), nil
}

// runCheckBefore clones the repository's HEAD into plan-check-base, shared
// with the repository's objects and checked out at HEAD's commit, so a check
// that asks git what changed sees nothing changed, and runs the command
// there. It says the exit status and what the command printed, stdout and
// stderr in turn.
func runCheckBefore(ctx context.Context, o Options, name string, args []string) (int, string, error) {
	if o.Scratch == "" || !isGitWorkTree(o.Root) {
		return 0, "", errors.New("not a git work tree with a scratch directory")
	}
	base := filepath.Join(o.Scratch, "plan-check-base")
	if err := os.RemoveAll(base); err != nil {
		return 0, "", err
	}
	gctx, cancel := context.WithTimeout(ctx, checkBeforeBudget)
	defer cancel()
	git := func(dir string, args ...string) (string, error) {
		cmd := exec.CommandContext(gctx, "git", args...)
		cmd.Dir = dir
		cmd.Env = scrubEnv(o.EnvAllow)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(string(out)))
		}
		return strings.TrimSpace(string(out)), nil
	}
	head, err := git(o.Root, "rev-parse", "HEAD")
	if err != nil {
		return 0, "", err
	}
	if _, err := git(o.Root, "clone", "-q", "--shared", "--no-checkout", o.Root, base); err != nil {
		return 0, "", err
	}
	if _, err := git(base, "checkout", "-q", "--detach", head); err != nil {
		return 0, "", err
	}
	timeout := checkBeforeBudget
	if dl, ok := gctx.Deadline(); ok {
		timeout = time.Until(dl)
	}
	return runCaptured(ctx, o, base, name, args, timeout)
}

// runCaptured runs a command in dir as shell-exec runs one, stopped after
// timeout, and says its exit status and what it printed, stdout and stderr
// in turn, up to their last checkBeforeKeep bytes each.
func runCaptured(ctx context.Context, o Options, dir, name string, args []string, timeout time.Duration) (int, string, error) {
	cctx, ccancel := context.WithTimeout(ctx, timeout)
	defer ccancel()
	cmd := exec.CommandContext(cctx, name, pipefail(name, args)...)
	cmd.Dir = dir
	cmd.Env = scrubEnv(o.EnvAllow)
	if o.Scratch != "" {
		cmd.Env = append(cmd.Env, "TMPDIR="+o.Scratch)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	var stdout, stderr checkBeforeOutput
	err := runStopping(cmd, &stdout, &stderr)
	if ctx.Err() != nil {
		return 0, "", ctx.Err()
	}
	if cctx.Err() != nil {
		return 0, "", fmt.Errorf("timed out after %s", timeout)
	}
	output := string(stdout.b) + "\n" + string(stderr.b)
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return 0, output, nil
	case errors.As(err, &exitErr):
		status := exitErr.ExitCode()
		if status < 0 {
			status = 128
		}
		return status, output, nil
	}
	return 0, "", err
}

// missingProgramREs are how a shell, make and Go's exec say a program is not
// installed. On commit-bench every attempt of every build met make
// validate's "make[1]: staticcheck: No such file or directory", and spent up
// to 19 calls looking for, building or installing it (2026-10-13).
var missingProgramREs = []*regexp.Regexp{
	regexp.MustCompile(`(?m)\b([A-Za-z0-9_.+-]+): command not found`),
	regexp.MustCompile(`(?m)\b(?:sh|bash|dash|zsh)(?:: line \d+|: \d+)?: ([A-Za-z0-9_.+-]+): not found`),
	regexp.MustCompile(`(?m)\bmake(?:\[\d+\])?: ([A-Za-z0-9_.+-]+): No such file or directory`),
	regexp.MustCompile(`exec: "([A-Za-z0-9_.+-]+)": executable file not found`),
}

// missingPrograms is the programs a command's output says are not
// installed, once each, in order.
func missingPrograms(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, re := range missingProgramREs {
		for _, m := range re.FindAllStringSubmatch(text, -1) {
			if !seen[m[1]] {
				seen[m[1]] = true
				out = append(out, m[1])
			}
		}
	}
	return out
}

func missingProgramsPrim() *scheme.Prim {
	return &scheme.Prim{
		Name: "missing-programs",
		Meta: scheme.Meta{
			Doc:    "The programs a command's output says are not installed (a shell's \"command not found\", make's \"make: prog: No such file or directory\", Go's \"executable file not found\"), once each, in order.",
			Params: []scheme.Param{{Name: "text", Type: "string", Doc: "the output"}},
			Hidden: true,
		},
		Handler: func(c *scheme.Call) (scheme.Value, error) {
			text, err := c.String(0)
			if err != nil {
				return scheme.Value{}, err
			}
			names := missingPrograms(text)
			out := make([]scheme.Value, len(names))
			for i, n := range names {
				out[i] = scheme.Str(n)
			}
			return scheme.List(out...), nil
		},
	}
}

// planRunBudget is how long (plan-run ...) may run a planned test, and
// planRunTail how much of its output's end it gives back.
var planRunBudget = 3 * time.Minute

// planRunMax is the longest a caller may give it: a repository's whole
// check (make validate on commit-bench) runs for minutes.
var planRunMax = 15 * time.Minute

const planRunTail = 2000

// planRun builds (plan-run cmd args): run a plan's command in the working
// tree, as shell-exec would, for the harness: after an edit the plan's tests
// run without the model asking (planspec.scm).
func planRun(o Options) *scheme.Prim {
	return &scheme.Prim{
		Name: "plan-run",
		Meta: scheme.Meta{
			Doc: "Run a plan's command in the working tree within " + planRunBudget.String() + ", as shell-exec runs one. Returns an alist of status, its exit status, and tail, the end of what it printed (stdout, then stderr). Fails when it could not run. Hidden; registered in a git work tree; its command is judged by the guard as a command's is.",
			Params: []scheme.Param{
				{Name: "cmd", Type: "string", Doc: "the program", Role: "argv"},
				{Name: "args", Type: "list", Doc: "its arguments", Role: "argv"},
				{Name: "seconds", Type: "integer", Doc: "how long it may run, at most " + planRunMax.String() + " (default " + planRunBudget.String() + ")", Optional: true},
			},
			Host:   true,
			Hidden: true,
		},
		Handler: func(c *scheme.Call) (scheme.Value, error) {
			name, err := c.String(0)
			if err != nil {
				return scheme.Value{}, err
			}
			items, err := c.List(1)
			if err != nil {
				return scheme.Value{}, err
			}
			args := make([]string, len(items))
			for i, it := range items {
				if it.Kind != scheme.KString {
					return scheme.Value{}, errors.New("args must be a list of strings")
				}
				args[i] = it.S
			}
			budget := planRunBudget
			if len(c.Args) > 2 {
				n, err := c.Int(2)
				if err != nil {
					return scheme.Value{}, err
				}
				if budget = time.Duration(n) * time.Second; budget <= 0 || budget > planRunMax {
					budget = planRunMax
				}
			}
			status, output, err := runCaptured(c.Ctx, o, o.Root, name, args, budget)
			if err != nil {
				return scheme.Value{}, fmt.Errorf("plan-run: %w", err)
			}
			output = strings.TrimSpace(output)
			if len(output) > planRunTail {
				output = "…" + output[len(output)-planRunTail:]
			}
			return scheme.List(
				scheme.Cons(scheme.Str("status"), scheme.Int(int64(status))),
				scheme.Cons(scheme.Str("tail"), scheme.Str(output)),
			), nil
		},
	}
}

// headHasNamePrim builds (head-has-name name): whether a file of HEAD holds
// name whole, as plan-evidence decides a name the change need not produce.
func headHasNamePrim(o Options) *scheme.Prim {
	return &scheme.Prim{
		Name: "head-has-name",
		Meta: scheme.Meta{
			Doc:    "Whether a file of HEAD holds name whole and in any case (session-id is not in X-Session-Id): a name the code already has. Registered in a git work tree.",
			Params: []scheme.Param{{Name: "name", Type: "string", Doc: "the name"}},
			Host:   true,
			Hidden: true,
		},
		Handler: func(c *scheme.Call) (scheme.Value, error) {
			name, err := c.String(0)
			if err != nil {
				return scheme.Value{}, err
			}
			return scheme.Bool(!strings.ContainsAny(name, " \t\n") && headHasName(c.Ctx, o, name)), nil
		},
	}
}

// docsMentioningMax is how many documents (docs-mentioning ...) names, and
// docsMentioningCommon how many documents a name may be in before it is too
// common to say anything (a word every ADR uses).
const (
	docsMentioningMax    = 4
	docsMentioningCommon = 8
)

// docsMentioning is the repository's documents (Markdown, LaTeX,
// reStructuredText, AsciiDoc, as HEAD has them) that hold one of names
// whole, with the names each holds, most names first: where the behaviour a
// change alters is written down. On commit-bench (dsl9 3422faf1) the plan
// said no document changed; the reference and the build without plans
// amended ADR 25, which describes Scanner.Scan and the guard's TextSecret
// the change altered (2026-10-13).
func docsMentioning(ctx context.Context, o Options, names []string) ([][2]string, error) {
	type doc struct {
		path  string
		names []string
	}
	byPath := map[string]*doc{}
	var order []string
	for _, n := range names {
		if strings.ContainsAny(n, " \t\n") || len(n) < 4 {
			continue
		}
		out, err := runGitLimit(ctx, o, 1<<20, "grep", "-I", "-i", "-F", "-l", "-e", n, "HEAD", "--", "*.md", "*.tex", "*.rst", "*.adoc")
		if err != nil {
			continue
		}
		files := strings.Fields(out)
		var holding []string
		for _, f := range files {
			if len(holding) > docsMentioningCommon {
				break
			}
			blob, err := runGitLimit(ctx, o, withoutMaxFile, "show", f)
			if err == nil && hasName(blob, n) {
				holding = append(holding, strings.TrimPrefix(f, "HEAD:"))
			}
		}
		if len(holding) > docsMentioningCommon {
			continue
		}
		for _, p := range holding {
			d := byPath[p]
			if d == nil {
				d = &doc{path: p}
				byPath[p] = d
				order = append(order, p)
			}
			d.names = append(d.names, n)
		}
	}
	docs := make([]*doc, 0, len(order))
	for _, p := range order {
		docs = append(docs, byPath[p])
	}
	sort.SliceStable(docs, func(i, j int) bool { return len(docs[i].names) > len(docs[j].names) })
	var res [][2]string
	for _, d := range docs {
		if len(res) == docsMentioningMax {
			break
		}
		res = append(res, [2]string{d.path, strings.Join(d.names, ", ")})
	}
	return res, nil
}

func docsMentioningPrim(o Options) *scheme.Prim {
	return &scheme.Prim{
		Name: "docs-mentioning",
		Meta: scheme.Meta{
			Doc:    "The repository's documents (Markdown, LaTeX, reStructuredText, AsciiDoc, as HEAD has them) that hold one of names whole, most names first, at most " + fmt.Sprint(docsMentioningMax) + ": a list of (path . \"the names it holds\"). A name in more than " + fmt.Sprint(docsMentioningCommon) + " documents, or shorter than 4 characters, is left out. Registered in a git work tree.",
			Params: []scheme.Param{{Name: "names", Type: "list", Doc: "the names, such as the functions a change alters"}},
			Host:   true,
			Hidden: true,
		},
		Handler: func(c *scheme.Call) (scheme.Value, error) {
			items, err := c.List(0)
			if err != nil {
				return scheme.Value{}, err
			}
			var names []string
			for _, it := range items {
				if it.Kind == scheme.KString {
					names = append(names, it.S)
				}
			}
			docs, err := docsMentioning(c.Ctx, o, names)
			if err != nil {
				return scheme.Value{}, fmt.Errorf("docs-mentioning: %w", err)
			}
			out := make([]scheme.Value, len(docs))
			for i, d := range docs {
				out[i] = scheme.Cons(scheme.Str(d[0]), scheme.Str(d[1]))
			}
			return scheme.List(out...), nil
		},
	}
}
