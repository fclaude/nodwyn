package prim

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/fclaude/brote/scheme"
)

// original-tests runs the check again with the existing tests a turn edited
// as the last commit had them, and says which tests then fail that the
// unchanged repository passes. A grader does what this does: the
// maintainers' test files are laid over the patch, and a test a patch had
// only made pass by rewriting it fails there. On commit-bench 5040f9fe asked
// for the response's Content-Type to be added to an error; the model reworded
// the error so that a phrase an existing test pins was split, edited that
// test's expectation to match, and every run of its own passed, while the
// original test failed against the patch (ADR 32, amended 2026-10-16). On
// SWE-bench's pytest-5787 and scikit-learn-13124 the same happened in
// Python, and nothing was asked, since the run was Go's alone (ADR 32,
// amended 2026-10-16). The test-edit nudge asks about the edit as it is made;
// this is the last place to ask, with a failing test to point at.
//
// With no edited test file to put back (paths empty) the first run is the
// check as the turn left it, and the second says which of its failures the
// unchanged repository does not have: the tests the change broke, when the
// model put a run's failures down to the environment.
//
// It needs no notion of a language: the check is the one the model itself
// ran and passed (the evidence loop passes its program and arguments, as for
// check-without), and the tests are named from what the run prints (pytest's
// FAILED lines, unittest's FAIL: and ERROR:, go test's --- FAIL:, cargo's
// ... FAILED, jest's ● lines, rspec's rspec lines: failureIDs). The test
// files the turn edited are written as HEAD has them, new test files are
// taken away (the grader does not have them), the check runs, and every file
// goes back to the bytes the turn left, after each run and again at the end
// whatever happens. If that run fails, the check runs once more with every
// changed file as HEAD had it, and a test that fails there too was not
// broken by the change and is not named. A run whose output names no test is
// told by its exit status alone.
//
// It runs the repository's test code, which whoever can commit to the
// repository writes, a stranger's clone included. That is what the model's own
// run of the same check did, in the environment a shell call gets and with
// the same approvals; here the harness starts it again, only for a check the
// model passed, and only in a turn the evidence loop runs, an unattended one
// or a supervised run's (ADR 45: trust is not a sandbox, and this is no new
// authority).

// originalTestsBudget is the time all of a call's runs share.
var originalTestsBudget = 5 * time.Minute

const (
	// originalMaxFiles is the most changed files the unchanged run puts
	// back; more, and it is not made.
	originalMaxFiles = 400
	// originalOutputMax is the output of a run that is read for failures:
	// the first and the last of it, since a run's end holds its summary.
	originalOutputMax  = 4 << 20
	originalOutputTail = 1 << 20
	// originalMaxNames is the tests named for one file.
	originalMaxNames = 12
)

// originalFailure is the tests that fail with the edited test files as HEAD
// has them, and not on the unchanged repository, under the file they belong
// to (or all the edited files, when the output does not say).
type originalFailure struct {
	path  string
	tests []string
}

// originalResult is what original-tests found: the failures, and why a run
// could not tell (the check did not finish in time or did not start, or it
// failed in the unchanged repository without naming a test).
type originalResult struct {
	failures []originalFailure
	unrun    []string
}

func originalTestsPrim(o Options) *scheme.Prim {
	return &scheme.Prim{
		Name: "original-tests",
		Meta: scheme.Meta{
			Doc: "Run a check again with the existing test files the turn edited as the last commit had them (new test files taken away), and name the tests that then fail and pass on the unchanged repository. The check runs within " + originalTestsBudget.String() + " in all: once with HEAD's version of each of paths that HEAD has and the working tree edited or deleted; if that run fails, once more with every changed file as HEAD had it, and a test that fails there too is left out. Tests are named from the run's output (pytest, unittest and Django, go test, cargo, jest, rspec); a run that fails without naming a test, where the unchanged repository passes, is named by its exit status. With an empty paths the check runs as the turn left it and only the second run is made, to name the tests the change itself breaks. Every file is put back to the bytes the turn left after each run. Returns an alist: failures, a list of alists of path (the edited test file the tests belong to, or all the edited files, comma-separated; empty when paths was empty) and tests (their names, at most " + fmt.Sprint(originalMaxNames) + "), and unrun, a list of strings saying why a run could not tell. Hidden; registered in a git work tree. The finish review names the failures (ADR 32, amended 2026-10-16).",
			Params: []scheme.Param{
				{Name: "cmd", Type: "string", Doc: "the check's program, as it was run", Role: "argv"},
				{Name: "args", Type: "list", Doc: "its arguments", Role: "argv"},
				{Name: "paths", Type: "list", Doc: "repository paths of the test files the turn edited"},
			},
			Host:   true,
			Hidden: true,
		},
		Handler: func(c *scheme.Call) (scheme.Value, error) {
			name, err := c.String(0)
			if err != nil {
				return scheme.Value{}, err
			}
			args, err := stringList(c, 1)
			if err != nil {
				return scheme.Value{}, err
			}
			rels, err := stringList(c, 2)
			if err != nil {
				return scheme.Value{}, err
			}
			res, err := originalTests(c.Ctx, o, name, args, rels)
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
			for _, why := range res.unrun {
				unrun = append(unrun, scheme.Str(why))
			}
			return scheme.List(
				scheme.Cons(scheme.Str("failures"), scheme.List(failures...)),
				scheme.Cons(scheme.Str("unrun"), scheme.List(unrun...))), nil
		},
	}
}

// originalFile is a file the runs change: how the turn left it (absent when
// it is not there), and the bytes HEAD has (when it has them).
type originalFile struct {
	rel      string
	abs      string
	now      []byte
	mode     fs.FileMode
	mtime    time.Time
	absent   bool
	head     []byte
	inHead   bool
	headBin  fs.FileMode
	headTime time.Time
}

// putBack makes the file what the turn left.
func (f originalFile) putBack(k *Keeper) error {
	if !k.inside(f.abs) {
		return errors.New("outside the repository")
	}
	if f.absent {
		if err := os.Remove(f.abs); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(f.abs), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(f.abs, f.now, f.mode); err != nil {
		return err
	}
	if err := os.Chmod(f.abs, f.mode); err != nil {
		return err
	}
	// With the time it had, so a cache keyed on a source's time and size
	// (Python's .pyc) of the turn's version is still the turn's.
	return os.Chtimes(f.abs, f.mtime, f.mtime)
}

// asHead makes the file what HEAD has, or takes it away when HEAD does not
// have it.
func (f originalFile) asHead(k *Keeper) error {
	if !k.inside(f.abs) {
		return errors.New("outside the repository")
	}
	if !f.inHead {
		if err := os.Remove(f.abs); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(f.abs), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(f.abs, f.head, f.headBin); err != nil {
		return err
	}
	if err := os.Chmod(f.abs, f.headBin); err != nil {
		return err
	}
	// And a time that is not the turn's version's: a cache keyed on a
	// source's time and size must not take one version for the other, as
	// two edits of the same size within a second would make it.
	return os.Chtimes(f.abs, f.headTime, f.headTime)
}

// read fills in how the turn left the file; it reports whether the file is
// one the runs may touch (a regular file under the size limit, or not there).
func (f *originalFile) read() bool {
	info, err := os.Lstat(f.abs)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		f.absent = true
		return true
	case err != nil || !info.Mode().IsRegular() || info.Size() > withoutMaxFile:
		return false
	}
	data, err := os.ReadFile(f.abs)
	if err != nil {
		return false
	}
	f.now, f.mode, f.mtime = data, info.Mode().Perm(), info.ModTime()
	return true
}

func originalTests(ctx context.Context, o Options, name string, args, rels []string) (originalResult, error) {
	var res originalResult
	k := &Keeper{o: o}
	_, changed, err := k.look(ctx)
	if err != nil {
		return res, err
	}
	tracked, err := k.headFiles(ctx)
	if err != nil {
		return res, err
	}
	deny := denylist(o)
	// The time HEAD's versions are written with: the same for every file and
	// run of a call, and not any version's own.
	headTime := time.Now().Add(-time.Hour).Truncate(time.Second)
	file := func(rel string) (*originalFile, bool) {
		if !keepable(deny, rel) {
			return nil, false
		}
		f := &originalFile{rel: rel, abs: filepath.Join(o.Root, filepath.FromSlash(rel)), headTime: headTime}
		if !f.read() {
			return nil, false
		}
		if h, ok := tracked[rel]; ok {
			blob, err := k.headBlob(ctx, h.oid)
			if err != nil || len(blob) > withoutMaxFile {
				return nil, false
			}
			f.head, f.inHead, f.headBin = blob, true, fs.FileMode(h.mode)
		}
		return f, true
	}
	// The existing test files the turn edited or deleted.
	var edited []*originalFile
	seen := map[string]bool{}
	for _, rel := range rels {
		rel = path.Clean(filepath.ToSlash(rel))
		if seen[rel] {
			continue
		}
		seen[rel] = true
		f, ok := file(rel)
		if !ok || !f.inHead || (!f.absent && bytes.Equal(f.now, f.head)) {
			continue
		}
		edited = append(edited, f)
	}
	// With no paths given there is nothing to put back: the check runs as the
	// turn left it, and only the last commit's run is held against it, to name
	// the tests the change itself broke (a run that failed, whose failures the
	// model put down to the environment: SWE-bench's psf-requests-2931).
	regress := len(rels) == 0
	if len(edited) == 0 && !regress {
		return res, nil
	}
	// The test files the turn made are not the grader's.
	var made []*originalFile
	for _, rel := range changed {
		if seen[rel] || !testPath(rel) {
			continue
		}
		if f, ok := file(rel); ok && !f.inHead && !f.absent {
			made = append(made, f)
		}
	}
	touched := append(append([]*originalFile(nil), edited...), made...)
	if regress {
		touched = nil
	}
	// Every file goes back to what the turn left, whatever happens.
	defer func() {
		for _, f := range touched {
			f.putBack(k)
		}
	}()
	deadline := time.Now().Add(originalTestsBudget)
	for _, f := range touched {
		if err := f.asHead(k); err != nil {
			return res, fmt.Errorf("could not put %s as the last commit had it: %w", f.rel, err)
		}
	}
	with, err := captureCheck(ctx, o, name, args, deadline)
	for _, f := range touched {
		if perr := f.putBack(k); perr != nil {
			return res, fmt.Errorf("could not put %s back: %w", f.rel, perr)
		}
	}
	if ctx.Err() != nil {
		return res, ctx.Err()
	}
	if err != nil {
		res.unrun = append(res.unrun, "the check did not finish with the test files as the last commit had them: "+err.Error())
		return res, nil
	}
	ids := failureIDs(with.output)
	if with.status == 0 && len(ids) == 0 {
		return res, nil
	}
	// It fails: does the unchanged repository fail it too?
	for _, rel := range changed {
		if seen[rel] || (!regress && slices.ContainsFunc(made, func(f *originalFile) bool { return f.rel == rel })) {
			continue
		}
		if f, ok := file(rel); ok {
			touched = append(touched, f)
		}
	}
	if len(touched) > originalMaxFiles {
		res.unrun = append(res.unrun, fmt.Sprintf("the change touches %d files, too many to put back", len(touched)))
		return res, nil
	}
	for _, f := range touched {
		if err := f.asHead(k); err != nil {
			return res, fmt.Errorf("could not put %s as the last commit had it: %w", f.rel, err)
		}
	}
	without, err := captureCheck(ctx, o, name, args, deadline)
	for _, f := range touched {
		if perr := f.putBack(k); perr != nil {
			return res, fmt.Errorf("could not put %s back: %w", f.rel, perr)
		}
	}
	if ctx.Err() != nil {
		return res, ctx.Err()
	}
	if err != nil {
		res.unrun = append(res.unrun, "the check did not finish on the last commit: "+err.Error())
		return res, nil
	}
	var named []string
	for _, id := range namesNotIn(ids, failureIDs(without.output)) {
		if !madeTestID(id, made) {
			named = append(named, id)
		}
	}
	switch {
	case len(named) > 0:
	case len(ids) == 0 && with.status != 0 && without.status == 0:
		named = []string{fmt.Sprintf("the check exits %d%s", with.status, failureTail(with.output))}
	case len(ids) == 0 && with.status != 0:
		res.unrun = append(res.unrun, "the check fails on the last commit too, and its output names no test")
		return res, nil
	default:
		return res, nil
	}
	rels2 := make([]string, len(edited))
	for i, f := range edited {
		rels2[i] = f.rel
	}
	res.failures = groupFailures(named, rels2)
	return res, nil
}

// captureCheck runs the check and keeps its output, the first and last of it.
func captureCheck(ctx context.Context, o Options, name string, args []string, deadline time.Time) (checkText, error) {
	buf := &bytes.Buffer{}
	out := &capped{buf: buf, n: originalOutputMax, tail: originalOutputTail}
	status, err := execCheck(ctx, o, name, args, deadline, out, out)
	out.finish()
	return checkText{status: status, output: buf.String()}, err
}

// checkText is how a run of the check ended and what it printed.
type checkText struct {
	status int
	output string
}

// failureTail is the last line a run printed, for a failure the output names no
// test of: " (last line: ...)", or nothing.
func failureTail(out string) string {
	lines := strings.Split(strings.TrimRight(out, "\n \t"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			if len(l) > 160 {
				l = l[:160] + "…"
			}
			return " (last line: " + l + ")"
		}
	}
	return ""
}

// namesNotIn is the names that are not in drop, in order.
func namesNotIn(names, drop []string) []string {
	var out []string
	for _, n := range names {
		if !slices.Contains(drop, n) {
			out = append(out, n)
		}
	}
	return out
}

// madeTestID is whether a failing test's name starts with the path of a test
// file the turn made (pytest's path::test): the grader does not have it.
func madeTestID(id string, made []*originalFile) bool {
	for _, f := range made {
		if strings.HasPrefix(id, f.rel+"::") || strings.HasPrefix(id, f.rel+" ") || strings.HasPrefix(id, "./"+f.rel) {
			return true
		}
	}
	return false
}

// groupFailures files each test under the edited test file its name starts
// with (pytest's path::test), the others under all the edited files.
func groupFailures(names, edited []string) []originalFailure {
	by := map[string][]string{}
	all := strings.Join(edited, ", ")
	for _, n := range names {
		key := all
		for _, rel := range edited {
			if strings.HasPrefix(n, rel+"::") || strings.HasPrefix(n, rel+":") {
				key = rel
				break
			}
		}
		by[key] = append(by[key], n)
	}
	var out []originalFailure
	for key, ns := range by {
		if len(ns) > originalMaxNames {
			ns = append(ns[:originalMaxNames:originalMaxNames], fmt.Sprintf("and %d more", len(ns)-originalMaxNames))
		}
		out = append(out, originalFailure{path: key, tests: ns})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out
}

// testPath is whether a repository path is a test file by its name or
// folder: x_test.go, test_x.py, x.test.js, x.spec.ts, or a file under a test,
// tests or __tests__ folder.
func testPath(rel string) bool {
	base := path.Base(rel)
	if strings.Contains(base, "_test.") || strings.Contains(base, ".test.") || strings.Contains(base, ".spec.") || strings.HasPrefix(base, "test_") {
		return true
	}
	for _, part := range strings.Split(path.Dir(rel), "/") {
		if part == "test" || part == "tests" || part == "__tests__" {
			return true
		}
	}
	return false
}

var (
	// rspec prints "rspec ./spec/x_spec.rb:12 # description" for each failure.
	rspecLine = regexp.MustCompile(`^rspec (\S+:\d+)`)
	// cargo test prints "test module::name ... FAILED".
	cargoLine = regexp.MustCompile(`^test (\S+) \.\.\. FAILED`)
)

// failureIDs is the tests a run's output says failed, each once, in the
// order the output gives them: go test's "--- FAIL: TestX" (a sub-test as
// TestX/case, only when its test is not named too) and, for a package that
// did not build or start, "package <path>" from "FAIL<tab>package<tab>"
// (only when no test is named); pytest's "FAILED path::test - why" and
// "ERROR path::test"; unittest's and Django's "FAIL: test (module.Class)" and
// "ERROR: ..."; cargo's "test x ... FAILED"; jest's "● suite › case";
// vitest's "FAIL path > suite > case"; rspec's "rspec ./spec/x.rb:12".
// Other output gives none.
func failureIDs(output string) []string {
	var names, packages []string
	add := func(list *[]string, id string) {
		if id != "" && !slices.Contains(*list, id) {
			*list = append(*list, id)
		}
	}
	firstWord := func(s string) string {
		w, _, _ := strings.Cut(strings.TrimSpace(s), " ")
		return w
	}
	for _, line := range strings.Split(output, "\n") {
		l := strings.TrimSpace(strings.TrimRight(line, "\r"))
		switch {
		case strings.HasPrefix(l, "--- FAIL: "):
			add(&names, firstWord(strings.TrimPrefix(l, "--- FAIL: ")))
		case strings.HasPrefix(l, "FAIL\t"):
			if f := strings.Split(l, "\t"); len(f) > 1 {
				add(&packages, "package "+firstWord(f[1]))
			}
		case strings.HasPrefix(l, "FAILED "):
			// pytest's summary line; unittest's "FAILED (failures=1)" is not a test.
			if w := firstWord(strings.TrimPrefix(l, "FAILED ")); strings.Contains(w, "::") {
				add(&names, w)
			}
		case strings.HasPrefix(l, "ERROR "):
			if w := firstWord(strings.TrimPrefix(l, "ERROR ")); strings.Contains(w, "::") || strings.Contains(w, "/") || strings.HasSuffix(w, ".py") {
				add(&names, w)
			}
		case strings.HasPrefix(l, "FAIL: "), strings.HasPrefix(l, "ERROR: "):
			_, rest, _ := strings.Cut(l, ": ")
			add(&names, strings.TrimSpace(rest))
		case strings.HasPrefix(l, "FAIL ") && strings.Contains(l, " > "):
			add(&names, strings.TrimSpace(strings.TrimPrefix(l, "FAIL ")))
		case strings.HasPrefix(l, "● ") && strings.Contains(l, " › "):
			add(&names, strings.TrimSpace(strings.TrimPrefix(l, "● ")))
		default:
			if m := rspecLine.FindStringSubmatch(l); m != nil {
				add(&names, m[1])
			} else if m := cargoLine.FindStringSubmatch(l); m != nil {
				add(&names, m[1])
			}
		}
	}
	var out []string
	for _, n := range names {
		if parent, _, ok := strings.Cut(n, "/"); ok && slices.Contains(names, parent) && !strings.Contains(parent, "::") {
			continue // a sub-test of a test already named
		}
		out = append(out, n)
	}
	if len(out) == 0 {
		return packages
	}
	return out
}
