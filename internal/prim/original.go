package prim

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/fclaude/nodwyn/internal/scheme"
)

// original-tests runs the existing tests a turn edited as the last commit
// had them, against the change as it is. A grader does what this does: the
// maintainers' test files are laid over the patch, and a test a patch had
// only made pass by rewriting it fails there. On commit-bench 5040f9fe asked
// for the response's Content-Type to be added to an error; the model reworded
// the error so that a phrase an existing test pins was split, edited that
// test's expectation to match, and every run of its own passed, while the
// original test failed against the patch (ADR 32, amended 2026-10-16). The
// test-edit nudge asks about the edit as it is made; this is the last place
// to ask, with a failing test to point at.
//
// Go only: go test -overlay puts HEAD's version of a file in place of the
// working tree's for the build and touches nothing on disk, so there is
// nothing to put back if the run is stopped. Another language's tests would
// need a copy of the tree or an edit in place; the loop asks nothing for
// them, and says so in its log. Only the tests HEAD's version declares are
// run, so a test the turn added to the file, or in a file of its own, is not
// asked about. A test that also fails with every changed Go file as HEAD had
// it was not broken by the change and is not named.
//
// It runs the repository's test code, which whoever can commit to the
// repository writes, a stranger's clone included. That is what the model's own
// go test of the same package does, in the environment a shell call gets and
// with the same approvals; here the harness starts it, with GOPROXY=off, for
// the package of a test file the model itself edited, and only in a turn the
// evidence loop runs, an unattended one or a supervised run's (ADR 45: trust
// is not a sandbox, and this is no new authority).

// originalTestsBudget is the time all of a call's runs share.
var originalTestsBudget = 3 * time.Minute

// originalFailure is the tests of one edited test file, as HEAD declares
// them, that fail against the change.
type originalFailure struct {
	path  string
	tests []string
}

// originalResult is what original-tests found: the failures, and the
// directories whose tests did not build with HEAD's files in place or did not
// finish in time, of which nothing is said.
type originalResult struct {
	failures []originalFailure
	unrun    []string
}

func originalTestsPrim(o Options) *scheme.Prim {
	return &scheme.Prim{
		Name: "original-tests",
		Meta: scheme.Meta{
			Doc:    "Run the existing tests the turn edited as the last commit had them, against the change as it is. For each of paths that is a Go test file (x_test.go) HEAD has and the working tree edited or deleted, the tests HEAD's version declares run with HEAD's version in place of the working tree's (go test -overlay, nothing on disk changes, no network), within " + originalTestsBudget.String() + " in all; a test that also fails with every changed Go file as HEAD had it is left out. Returns an alist: failures, a list of alists of path (the test file) and tests (the names of its tests that fail), and unrun, the directories whose tests did not build with HEAD's files in place or did not finish. Files HEAD does not have, and files that are not Go tests, are left out. Hidden; registered in a git work tree. The finish review names the failures (ADR 32, amended 2026-10-16).",
			Params: []scheme.Param{{Name: "paths", Type: "list", Doc: "repository paths of test files the turn edited"}},
			Host:   true,
			Hidden: true,
		},
		Handler: func(c *scheme.Call) (scheme.Value, error) {
			items, err := c.List(0)
			if err != nil {
				return scheme.Value{}, err
			}
			rels := make([]string, len(items))
			for i, it := range items {
				if it.Kind != scheme.KString {
					return scheme.Value{}, errors.New("paths must be a list of strings")
				}
				rels[i] = it.S
			}
			res, err := originalTests(c.Ctx, o, rels)
			if err != nil {
				return scheme.Value{}, fmt.Errorf("original-tests: %w", err)
			}
			var failures, unrun []scheme.Value
			for _, f := range res.failures {
				names := make([]scheme.Value, len(f.tests))
				for i, n := range f.tests {
					names[i] = scheme.Str(n)
				}
				failures = append(failures, scheme.List(
					scheme.Cons(scheme.Str("path"), scheme.Str(f.path)),
					scheme.Cons(scheme.Str("tests"), scheme.List(names...))))
			}
			for _, d := range res.unrun {
				unrun = append(unrun, scheme.Str(d))
			}
			return scheme.List(
				scheme.Cons(scheme.Str("failures"), scheme.List(failures...)),
				scheme.Cons(scheme.Str("unrun"), scheme.List(unrun...))), nil
		},
	}
}

// originalFile is an edited test file as HEAD has it, and the tests it
// declares.
type originalFile struct {
	rel   string
	head  []byte
	tests []string
}

func originalTests(ctx context.Context, o Options, rels []string) (originalResult, error) {
	var res originalResult
	k := &Keeper{o: o}
	_, changed, err := k.look(ctx)
	if err != nil {
		return res, err
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		return res, nil // no Go here: nothing to run
	}
	tracked, err := k.headFiles(ctx)
	if err != nil {
		return res, err
	}
	root, err := filepath.EvalSymlinks(o.Root)
	if err != nil {
		return res, err
	}
	deny := denylist(o)
	byDir := map[string][]originalFile{}
	seen := map[string]bool{}
	for _, rel := range rels {
		rel = path.Clean(filepath.ToSlash(rel))
		if seen[rel] || !strings.HasSuffix(rel, "_test.go") || !keepable(deny, rel) {
			continue
		}
		seen[rel] = true
		h, ok := tracked[rel]
		if !ok {
			continue // the turn added it
		}
		head, err := k.headBlob(ctx, h.oid)
		if err != nil || len(head) > withoutMaxFile {
			continue
		}
		info, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel)))
		switch {
		case errors.Is(err, fs.ErrNotExist): // deleted: its tests come back
		case err != nil || !info.Mode().IsRegular():
			continue
		default:
			cur, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
			if err != nil || bytes.Equal(cur, head) {
				continue // not edited
			}
		}
		if tests := declaredTests(head); len(tests) > 0 {
			byDir[path.Dir(rel)] = append(byDir[path.Dir(rel)], originalFile{rel: rel, head: head, tests: tests})
		}
	}
	if len(byDir) == 0 {
		return res, nil
	}
	if err := os.MkdirAll(o.Scratch, 0o755); err != nil {
		return res, err
	}
	tmp, err := os.MkdirTemp(o.Scratch, "original-tests-")
	if err != nil {
		return res, err
	}
	defer os.RemoveAll(tmp)
	serial := 0
	stage := func(data []byte) (string, error) {
		serial++
		p := filepath.Join(tmp, fmt.Sprintf("%d.go", serial))
		return p, os.WriteFile(p, data, 0o600)
	}
	replace := map[string]string{}
	declared := map[string]string{} // test name -> its file, within a directory
	dirs := make([]string, 0, len(byDir))
	for dir, files := range byDir {
		dirs = append(dirs, dir)
		for _, f := range files {
			p, err := stage(f.head)
			if err != nil {
				return res, err
			}
			replace[filepath.Join(root, filepath.FromSlash(f.rel))] = p
		}
	}
	sort.Strings(dirs)
	overlay, err := writeOverlay(tmp, "overlay.json", replace)
	if err != nil {
		return res, err
	}
	deadline := time.Now().Add(originalTestsBudget)
	failing := map[string][]string{} // dir -> names that fail
	for _, dir := range dirs {
		var names []string
		for _, f := range byDir[dir] {
			names = append(names, f.tests...)
			for _, n := range f.tests {
				declared[dir+"\x00"+n] = f.rel
			}
		}
		out, err := goTestOverlay(ctx, o, goBin, filepath.Join(root, filepath.FromSlash(dir)), overlay, names, deadline)
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		failed, ran := failedNames(out, names)
		switch {
		case err != nil || !ran:
			res.unrun = append(res.unrun, dir)
		case len(failed) > 0:
			failing[dir] = failed
		}
	}
	if len(failing) > 0 {
		// The tests that fail with every changed Go file as HEAD had it
		// were not broken by this change.
		base := map[string]string{}
		for abs, p := range replace {
			base[abs] = p
		}
		for _, rel := range changed {
			abs := filepath.Join(root, filepath.FromSlash(rel))
			if _, in := base[abs]; in || !strings.HasSuffix(rel, ".go") || !keepable(deny, rel) {
				continue
			}
			h, ok := tracked[rel]
			if !ok {
				base[abs] = "" // new: not there before
				continue
			}
			blob, err := k.headBlob(ctx, h.oid)
			if err != nil {
				continue
			}
			p, err := stage(blob)
			if err != nil {
				return res, err
			}
			base[abs] = p
		}
		baseOverlay, err := writeOverlay(tmp, "base.json", base)
		if err != nil {
			return res, err
		}
		for _, dir := range dirs {
			names := failing[dir]
			if len(names) == 0 {
				continue
			}
			out, err := goTestOverlay(ctx, o, goBin, filepath.Join(root, filepath.FromSlash(dir)), baseOverlay, names, deadline)
			if ctx.Err() != nil {
				return res, ctx.Err()
			}
			if before, ran := failedNames(out, names); err == nil && ran {
				failing[dir] = without(names, before)
			}
		}
	}
	byFile := map[string][]string{}
	for dir, names := range failing {
		for _, n := range names {
			byFile[declared[dir+"\x00"+n]] = append(byFile[declared[dir+"\x00"+n]], n)
		}
	}
	for rel, names := range byFile {
		sort.Strings(names)
		res.failures = append(res.failures, originalFailure{path: rel, tests: names})
	}
	sort.Slice(res.failures, func(i, j int) bool { return res.failures[i].path < res.failures[j].path })
	return res, nil
}

// without is names but for the ones in drop.
func without(names, drop []string) []string {
	var out []string
	for _, n := range names {
		keep := true
		for _, d := range drop {
			if d == n {
				keep = false
				break
			}
		}
		if keep {
			out = append(out, n)
		}
	}
	return out
}

// writeOverlay writes go's -overlay file for replace: an absolute path in the
// build, the file to read in its place ("" for a file that is not there).
func writeOverlay(dir, name string, replace map[string]string) (string, error) {
	data, err := json.Marshal(map[string]map[string]string{"Replace": replace})
	if err != nil {
		return "", err
	}
	p := filepath.Join(dir, name)
	return p, os.WriteFile(p, data, 0o600)
}

// declaredTests is the names of the tests a Go test file declares: go test's
// rule, Test and then nothing or a character that is not a lower-case letter
// (TestMain sets a package up and tests nothing).
func declaredTests(src []byte) []string {
	f, err := parser.ParseFile(token.NewFileSet(), "", src, parser.SkipObjectResolution)
	if err != nil {
		return nil
	}
	var out []string
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || !strings.HasPrefix(fn.Name.Name, "Test") || fn.Name.Name == "TestMain" {
			continue
		}
		if rest := fn.Name.Name[4:]; rest != "" {
			if r, _ := utf8.DecodeRuneInString(rest); unicode.IsLower(r) {
				continue
			}
		}
		out = append(out, fn.Name.Name)
	}
	return out
}

// goTestOverlay runs the named tests of the package in dir with the overlay
// in place, as shell-exec would run go test: the allowed environment, the
// session's scratch directory as TMPDIR, the whole process group stopped when
// it ends or time runs out. Nothing is fetched: a module the cache lacks
// fails the run, which is then unrun. Its output is stdout and stderr.
func goTestOverlay(parent context.Context, o Options, goBin, dir, overlay string, names []string, deadline time.Time) (string, error) {
	left := time.Until(deadline)
	if left <= 0 {
		return "", errors.New("out of time")
	}
	ctx, cancel := context.WithTimeout(parent, left)
	defer cancel()
	cmd := exec.CommandContext(ctx, goBin, "test", "-count=1", "-vet=off", "-overlay="+overlay, "-run", "^("+strings.Join(names, "|")+")$", ".")
	cmd.Dir = dir
	cmd.Env = append(scrubEnv(o.EnvAllow), "GOPROXY=off")
	if o.Scratch != "" {
		cmd.Env = append(cmd.Env, "TMPDIR="+o.Scratch)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	var stdout, stderr bytes.Buffer
	err := runStopping(cmd, &capped{buf: &stdout, n: 1 << 20}, &capped{buf: &stderr, n: 1 << 20})
	if parent.Err() != nil {
		return "", parent.Err()
	}
	if ctx.Err() != nil {
		return "", errors.New("out of time")
	}
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		return "", err
	}
	return stdout.String() + "\n" + stderr.String(), nil
}

// failedNames reads go test's output: the top-level tests of names that
// failed, and whether the tests ran at all (the package built and went
// through to an "ok" or a "FAIL" line of its own).
func failedNames(out string, names []string) (failed []string, ran bool) {
	asked := map[string]bool{}
	for _, n := range names {
		asked[n] = true
	}
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "--- FAIL: "):
			if name, _, _ := strings.Cut(strings.TrimPrefix(line, "--- FAIL: "), " "); asked[name] && !slices.Contains(failed, name) {
				failed = append(failed, name)
			}
		case strings.HasPrefix(line, "ok  \t"), strings.HasPrefix(line, "FAIL\t"):
			if !strings.Contains(line, "[build failed]") && !strings.Contains(line, "[setup failed]") {
				ran = true
			}
		}
	}
	return failed, ran
}
