package main

import (
	"archive/tar"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// A test written for a change fails on the code before it and passes on the
// code after: one that passes on both does not test the change, whatever
// its name says. On 2026-10-03 a session's test for "a clean close keeps
// attachments" passed on the code that deleted them, because its setup
// made the tree dirty for another reason. againstBase runs each changed Go
// test against a clean copy of the code at base, with the changed test
// files laid over it, and says which pass there.

type testFunc struct {
	name       string
	start, end int
}

// againstBase reports, for the tests the change adds or edits, which fail
// on the unchanged code (they test the change), which pass there (they do
// not test a fix), and which cannot say (they do not compile there, since
// they call code the change adds).
func againstBase(base string, changed []string, logs string) []result {
	tests, err := changedTests(base, changed)
	if err != nil {
		return []result{{status: "FAIL", name: "tests vs unchanged", detail: err.Error()}}
	}
	if len(tests) == 0 {
		return nil
	}
	copyDir, err := os.MkdirTemp("", "nodwyn-validate-base-")
	if err != nil {
		return []result{{status: "FAIL", name: "tests vs unchanged", detail: err.Error()}}
	}
	defer os.RemoveAll(copyDir)
	if err := extract(base, copyDir); err != nil {
		return []result{{status: "FAIL", name: "tests vs unchanged", detail: "copying the unchanged code: " + err.Error()}}
	}
	var bite, pass, unsure []string
	dirs := make([]string, 0, len(tests))
	for dir := range tests {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	for _, dir := range dirs {
		if err := overlay(changed, dir, copyDir); err != nil {
			return []result{{status: "FAIL", name: "tests vs unchanged", detail: err.Error()}}
		}
		names := tests[dir]
		logPath := filepath.Join(logs, "unchanged-"+strings.ReplaceAll(dir, "/", "-")+".log")
		_, timedOut, err := run(copyDir, logPath, 10*time.Minute, "go", "test", "-count=1", "-v", "-run", "^("+strings.Join(names, "|")+")$", "./"+dir+"/")
		raw, _ := os.ReadFile(logPath)
		got := outcomes(string(raw))
		for _, n := range names {
			id := dir + "." + n
			switch {
			case err != nil:
				unsure = append(unsure, id+" (could not run go: "+err.Error()+")")
			case got[n] == "FAIL":
				bite = append(bite, id)
			case got[n] == "PASS":
				pass = append(pass, id)
			case got[n] == "SKIP":
				unsure = append(unsure, id+" (skipped there)")
			case buildFailed(string(raw)):
				unsure = append(unsure, id+" (does not compile there)")
			case timedOut:
				unsure = append(unsure, id+" (stopped after 10m)")
			default:
				unsure = append(unsure, id+" (no result: "+logPath+")")
			}
		}
	}
	var out []result
	if len(pass) > 0 {
		out = append(out, result{status: "WARN", name: "tests vs unchanged", detail: "pass on the unchanged code, so they do not test a fix: " + strings.Join(pass, ", ") + ". Fix them, or say in your answer why they pass there"})
	}
	if len(bite) > 0 {
		out = append(out, result{status: "PASS", name: "tests vs unchanged", detail: "fail on the unchanged code: " + strings.Join(bite, ", ")})
	}
	if len(unsure) > 0 {
		out = append(out, result{status: "SKIP", name: "tests vs unchanged", detail: "cannot say: " + strings.Join(unsure, ", ")})
	}
	return out
}

// changedTests maps each package directory to the tests the change adds or
// edits: every test in a new test file, and each test whose lines differ
// from base.
func changedTests(base string, changed []string) (map[string][]string, error) {
	out := map[string][]string{}
	for _, p := range changed {
		if !strings.HasSuffix(p, "_test.go") || !fileExists(p) {
			continue
		}
		ranges, isNew, err := changedLines(base, p)
		if err != nil {
			return nil, err
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		dir := filepath.Dir(p)
		for _, t := range testFuncs(raw) {
			if (isNew || overlaps(t, ranges)) && !slices.Contains(out[dir], t.name) {
				out[dir] = append(out[dir], t.name)
			}
		}
	}
	for dir := range out {
		sort.Strings(out[dir])
	}
	return out, nil
}

// testFuncs lists the tests a file declares, with their lines.
func testFuncs(src []byte) []testFunc {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", src, 0)
	if err != nil {
		return nil
	}
	var out []testFunc
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || !isTestName(fn.Name.Name) {
			continue
		}
		out = append(out, testFunc{fn.Name.Name, fset.Position(fn.Pos()).Line, fset.Position(fn.End()).Line})
	}
	return out
}

// isTestName is go test's rule: Test, then nothing or a character that is
// not a lower-case letter. TestMain sets up a package; it tests nothing.
func isTestName(n string) bool {
	if n == "TestMain" || !strings.HasPrefix(n, "Test") {
		return false
	}
	if len(n) == 4 {
		return true
	}
	r, _ := utf8.DecodeRuneInString(n[4:])
	return !unicode.IsLower(r)
}

func overlaps(t testFunc, ranges [][2]int) bool {
	for _, r := range ranges {
		if r[0] <= t.end && t.start <= r[1] {
			return true
		}
	}
	return false
}

// extract writes the tree at base into dir, as git archive gives it.
func extract(base, dir string) error {
	cmd := exec.Command("git", "archive", "--format=tar", base)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	tr := tar.NewReader(stdout)
	root := filepath.Clean(dir) + string(filepath.Separator)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			_ = cmd.Wait()
			return err
		}
		target := filepath.Join(dir, h.Name)
		if !strings.HasPrefix(target+string(filepath.Separator), root) {
			_ = cmd.Wait()
			return fmt.Errorf("archive entry outside the copy: %s", h.Name)
		}
		switch h.Typeflag {
		case tar.TypeDir:
			err = os.MkdirAll(target, 0o755)
		case tar.TypeReg:
			err = writeFile(target, tr, os.FileMode(h.Mode)&0o777)
		case tar.TypeSymlink:
			if err = os.MkdirAll(filepath.Dir(target), 0o755); err == nil {
				err = os.Symlink(h.Linkname, target)
			}
		}
		if err != nil {
			_ = cmd.Wait()
			return err
		}
	}
	return cmd.Wait()
}

func writeFile(target string, r io.Reader, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// overlay lays the change's test files and test data for dir over the
// copy, and removes the ones the change deleted, so the copy is the
// unchanged code with the changed tests.
func overlay(changed []string, dir, copyDir string) error {
	for _, p := range changed {
		if !(filepath.Dir(p) == dir && strings.HasSuffix(p, "_test.go")) && !strings.HasPrefix(p, dir+"/testdata/") {
			continue
		}
		target := filepath.Join(copyDir, p)
		if !fileExists(p) {
			if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			continue
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		err = writeFile(target, f, 0o644)
		f.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

var testLine = regexp.MustCompile(`(?m)^--- (PASS|FAIL|SKIP): (\S+)`)

// outcomes reads go test -v output: each top-level test's result.
func outcomes(out string) map[string]string {
	got := map[string]string{}
	for _, m := range testLine.FindAllStringSubmatch(out, -1) {
		got[m[2]] = m[1]
	}
	return got
}

func buildFailed(out string) bool {
	return strings.Contains(out, "[build failed]") || strings.Contains(out, "[setup failed]")
}
