package prim

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
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
	markerRE = regexp.MustCompile(`^[A-Z][A-Z0-9_]*(\.[A-Za-z0-9]+)?$`)
	brandRE  = regexp.MustCompile(`^[A-Z][A-Za-z0-9]*$`)
	versionRE     = regexp.MustCompile(`^v?\d+([._-]\d+)*$`)
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
				if !hasName(text, n) {
					missing = append(missing, scheme.Str(n))
				}
			}
			for _, p := range paths {
				clean := filepath.ToSlash(filepath.Clean(strings.TrimPrefix(p, "./")))
				if !changed[clean] {
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
			Doc:    "Run a test on the code as it was before the change: a copy of HEAD in the scratch directory (plan-base) with the working tree's version of the file at path put in, the command run there within " + testBeforeBudget.String() + ". Returns its exit status, or fails when it could not run. Hidden; registered in a git work tree; its command is judged by the guard as a command's is.",
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
