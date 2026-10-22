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
	"time"

	"github.com/fclaude/brote/scheme"
)

// A plan can be written as Scheme data, one form per step (planspec.scm):
// what the plan names, the files, the tests and the commands that run them,
// and the names the change must produce, are then fields the harness reads
// instead of words in a sentence. These primitives are its host side: read
// the data without running it, take the names a request gives, and set a
// plan's names and files against the change as git shows it (ADR 32,
// amended 2026-10-16).

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
	// for names that did not matter (ADR 32, amended 2026-10-16).
	markerRE  = regexp.MustCompile(`^[A-Z][A-Z0-9_]*(\.[A-Za-z0-9]+)?$`)
	brandRE   = regexp.MustCompile(`^[A-Z][A-Za-z0-9]*$`)
	versionRE = regexp.MustCompile(`^v?\d+([._-]\d+)*$`)
	// Three more that are no names the change produces, from the harness
	// around a request or the environment it reports: a name with a version
	// inside (clang-1000.11.45.5, Darwin-18.2.0-x86_64: a platform string); a
	// product's name, capitals then a lower-case word (SWE-bench); and the
	// key of a "- key: value" line (instance_id, base_commit: the fields a
	// task template fills in). On SWE-bench's 121 tasks the planner was asked
	// for them again in every one of eight reviewed (ADR 32, amended
	// 2026-10-16).
	inlineVersionRE = regexp.MustCompile(`\d+\.\d+`)
	acronymBrandRE  = regexp.MustCompile(`^[A-Z]{2,}-[a-z]+$`)
	fieldKeyRE      = regexp.MustCompile(`(?m)^\s*[-*]\s+([A-Za-z][A-Za-z0-9_]*):`)
)

// requestNames is the names a request gives that read as code: spans in
// backquotes without a space, and words with a hyphen, an underscore or a
// dot inside, or a small letter first and a capital after it (session-id,
// cache_key, response.output_item.done, readResponses). Not a link, an issue
// number, a version, a word in capitals (markerRE) or a capitalised one
// without a separator (brandRE), a platform string, a product's name or a
// task template's field key.
func requestNames(text string) []string {
	var out []string
	seen := map[string]bool{}
	fields := map[string]bool{}
	for _, m := range fieldKeyRE.FindAllStringSubmatch(text, -1) {
		fields[m[1]] = true
	}
	for _, m := range requestNameRE.FindAllStringSubmatch(linkRE.ReplaceAllString(text, " "), -1) {
		s := strings.TrimSpace(m[1])
		if s == "" {
			s = m[2]
		}
		if s == "" || strings.ContainsAny(s, " \t") || strings.Contains(s, "://") || strings.HasPrefix(s, "www.") ||
			issueRefRE.MatchString(s) || versionRE.MatchString(s) || s == "e.g" || s == "i.e" ||
			markerRE.MatchString(s) || brandRE.MatchString(s) || acronymBrandRE.MatchString(s) || inlineVersionRE.MatchString(s) ||
			(m[1] == "" && fields[s]) {
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
	// The change's other test files and test support (a fake, a fixture)
	// go in too: the unchanged code is the code under test, not the tests.
	// On commit-bench (dsl13 107251d0) two tests that drive the change through
	// a fake it rewrote passed on a copy that had the old fake, and were said
	// not to show the change.
	k := &Keeper{o: o}
	if _, paths, err := k.look(ctx); err == nil {
		for _, p := range paths {
			p = filepath.Clean(filepath.FromSlash(p))
			if p == clean || !testSide(filepath.ToSlash(p)) {
				continue
			}
			b, err := os.ReadFile(filepath.Join(o.Root, p))
			if err != nil {
				continue // deleted by the change, or not a file
			}
			to := filepath.Join(base, p)
			if err := os.MkdirAll(filepath.Dir(to), 0o755); err == nil {
				_ = os.WriteFile(to, b, 0o644)
			}
		}
	}
	run := o
	run.Root = base
	r, err := runCheck(ctx, run, name, args, time.Now().Add(testBeforeBudget))
	if err != nil {
		return 0, err
	}
	return r.status, nil
}

// testSide is whether a repository path belongs to the tests rather than the
// code they test: a test file by its name (x_test.go, test_x.py, x_test.py,
// x.test.ts, x.spec.js) or a file under a directory of tests, fakes or
// fixtures.
func testSide(p string) bool {
	base := filepath.Base(p)
	if strings.Contains(base, "_test.") || strings.HasPrefix(base, "test_") || strings.Contains(base, ".test.") || strings.Contains(base, ".spec.") {
		return true
	}
	for _, d := range strings.Split(filepath.ToSlash(filepath.Dir(p)), "/") {
		switch d {
		case "test", "tests", "testdata", "fakes", "fixtures", "__tests__", "spec":
			return true
		}
	}
	return false
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
func docsMentioning(ctx context.Context, o Options, names, packages []string) ([][2]string, error) {
	type doc struct {
		path  string
		names []string
	}
	byPath := map[string]*doc{}
	var order []string
	add := func(p, n string) {
		d := byPath[p]
		if d == nil {
			d = &doc{path: p}
			byPath[p] = d
			order = append(order, p)
		}
		d.names = append(d.names, n)
	}
	// An ADR is named for what it decides (0025-ingress-redaction.md): one
	// whose name has a word starting with the name of a package the change
	// alters (redact) is about it, whether or not it names a function.
	if len(packages) > 0 {
		out, err := runGitLimit(ctx, o, 4<<20, "ls-tree", "-r", "--name-only", "HEAD")
		if err == nil {
			for _, p := range strings.Fields(out) {
				if !strings.Contains("/"+p, "/adr/") || !docExt(p) {
					continue
				}
				base := strings.ToLower(strings.TrimSuffix(filepath.Base(p), filepath.Ext(p)))
				words := strings.FieldsFunc(base, func(r rune) bool { return r == '-' || r == '_' || r == '.' })
				for _, pkg := range packages {
					pkg = strings.ToLower(pkg)
					if len(pkg) < 3 {
						continue
					}
					for _, w := range words {
						if strings.HasPrefix(w, pkg) {
							add(p, pkg)
							break
						}
					}
				}
			}
		}
	}
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
			add(p, n)
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

func docExt(p string) bool {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".md", ".tex", ".rst", ".adoc":
		return true
	}
	return false
}

func docsMentioningPrim(o Options) *scheme.Prim {
	return &scheme.Prim{
		Name: "docs-mentioning",
		Meta: scheme.Meta{
			Doc: "The repository's documents (Markdown, LaTeX, reStructuredText, AsciiDoc, as HEAD has them) that hold one of names whole, and the ADRs whose file name has a word starting with one of packages, most names first, at most " + fmt.Sprint(docsMentioningMax) + ": a list of (path . \"the names it holds\"). A name in more than " + fmt.Sprint(docsMentioningCommon) + " documents, or shorter than 4 characters, is left out. Registered in a git work tree.",
			Params: []scheme.Param{{Name: "names", Type: "list", Doc: "the names, such as the functions a change alters"},
				{Name: "packages", Type: "list", Doc: "the names of the packages (directories) the change alters: an ADR whose file name has a word starting with one is about it", Optional: true}},
			Host:   true,
			Hidden: true,
		},
		Handler: func(c *scheme.Call) (scheme.Value, error) {
			items, err := c.List(0)
			if err != nil {
				return scheme.Value{}, err
			}
			var names, packages []string
			for _, it := range items {
				if it.Kind == scheme.KString {
					names = append(names, it.S)
				}
			}
			if len(c.Args) > 1 {
				pkgs, err := c.List(1)
				if err != nil {
					return scheme.Value{}, err
				}
				for _, it := range pkgs {
					if it.Kind == scheme.KString {
						packages = append(packages, it.S)
					}
				}
			}
			docs, err := docsMentioning(c.Ctx, o, names, packages)
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

// gitIntend builds (git-intend paths): mark new, untracked, not ignored files
// of the working tree as intended for the next commit (git add -N), so tools
// that list the tracked files see them. The book's catalogue generator lists
// files with git ls-files: on commit-bench (dsl8 107251d0) a test file the
// model added was left out of the catalogue it had just regenerated, and both
// reviewers found the catalogue stale (2026-10-13). Nothing is staged: an
// intended file is an empty entry until it is added.
func gitIntend(o Options) *scheme.Prim {
	return &scheme.Prim{
		Name: "git-intend",
		Meta: scheme.Meta{
			Doc:    "Mark new files of the working tree as intended for the next commit (git add -N): those of paths that are untracked, inside the repository and not ignored. Returns the paths it marked. Hidden; registered in a git work tree.",
			Params: []scheme.Param{{Name: "paths", Type: "list", Doc: "repository paths"}},
			Host:   true,
			Hidden: true,
		},
		Handler: func(c *scheme.Call) (scheme.Value, error) {
			items, err := c.List(0)
			if err != nil {
				return scheme.Value{}, err
			}
			k := &Keeper{o: o}
			var marked []scheme.Value
			for _, it := range items {
				if it.Kind != scheme.KString {
					continue
				}
				rel := filepath.ToSlash(filepath.Clean(strings.TrimPrefix(it.S, "./")))
				if rel == "." || strings.HasPrefix(rel, "../") || filepath.IsAbs(rel) {
					continue
				}
				abs := filepath.Join(o.Root, filepath.FromSlash(rel))
				if !k.inside(abs) {
					continue
				}
				if info, err := os.Lstat(abs); err != nil || !info.Mode().IsRegular() {
					continue
				}
				if _, err := runGitLimit(c.Ctx, o, 4096, "ls-files", "--error-unmatch", "--", rel); err == nil {
					continue // tracked already
				}
				if _, err := runGitLimit(c.Ctx, o, 4096, "check-ignore", "-q", "--", rel); err == nil {
					continue // ignored
				}
				if _, err := runGitLimit(c.Ctx, o, 4096, "add", "-N", "--", rel); err == nil {
					marked = append(marked, scheme.Str(rel))
				}
			}
			return scheme.List(marked...), nil
		},
	}
}
