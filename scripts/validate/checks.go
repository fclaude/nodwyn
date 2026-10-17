package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// check is one of the commands AGENTS.md asks for, chosen by the files that
// changed.
type check struct {
	name string
	run  func(logs string) result
}

// plan chooses the checks the changed files need. Documentation no test
// reads needs none; anything else needs make accept, which is what
// promotion runs; the console, its checks and the code and fakes the checks
// run need the browser checks, since Go's tests do not run the console.
//
// The books are not built here: the release flow builds them (ADR 42), and
// building the book for the catalogue's counts cost about 80 s a run (ADR
// 37, amended 2026-10-07). Checking that the catalogue lists every Go and
// Scheme file a commit of the tree would hold takes under a second, so a Go
// or Scheme change, deleted files included, runs that: on commit-bench eight
// of twenty changes left it stale while validate said PASS (ADR 37, amended
// 2026-10-15). A change to the release publisher runs its tests, which only
// CI ran (ADR 37, amended 2026-10-16).
func plan(base string, changed []string) []check {
	var checks []check
	var goFiles, jsFiles []string
	accept, browser, catalogue, release := false, false, false, false
	for _, p := range changed {
		exists := fileExists(p)
		switch {
		case strings.HasSuffix(p, ".go") && exists:
			goFiles = append(goFiles, p)
		case strings.HasSuffix(p, ".js") && exists && (strings.HasPrefix(p, "internal/frontend/web/assets/") || strings.HasPrefix(p, "test/browser/checks/")):
			jsFiles = append(jsFiles, p)
		}
		if !docOnly(p) && !browserOnly(p) {
			accept = true
		}
		if browserNeeds(p) {
			browser = true
		}
		if strings.HasSuffix(p, ".go") || strings.HasSuffix(p, ".scm") || p == "docs/book/source/generate.py" {
			catalogue = true
		}
		if p == "scripts/release.py" || strings.HasPrefix(p, "test/release/") {
			release = true
		}
	}
	if len(goFiles) > 0 {
		checks = append(checks, check{"gofmt", func(string) result { return gofmtCheck(goFiles) }})
	}
	if len(jsFiles) > 0 {
		checks = append(checks, check{"node --check", func(string) result { return nodeCheck(jsFiles) }})
	}
	if catalogue {
		checks = append(checks, check{"book catalogue", bookInventory})
	}
	if release {
		checks = append(checks, check{"release tests", func(logs string) result {
			return step("release tests", "python3 -m unittest discover -s test/release", logs, 5*time.Minute, "python3", "-B", "-m", "unittest", "discover", "-s", "test/release")
		}})
	}
	if accept {
		checks = append(checks, check{"make accept", func(logs string) result {
			return step("make accept", "go vet, staticcheck, lints, go test -race ./..., exfil", logs, 30*time.Minute, "make", "accept")
		}})
	}
	if browser {
		checks = append(checks, check{"browser checks", func(logs string) result { return browserChecks(base, logs) }})
	}
	return checks
}

// browserNeeds is whether a change to p can change what a browser check
// sees: the console and its checks, and the host code, event shapes and
// fakes the checks' demo runs. Most checks start the real daemon, so a path
// map of which check reads what would be wrong the day a check starts
// reading one more package; the whole suite takes about 30 s. A Go test
// file is no part of what a check runs.
func browserNeeds(p string) bool {
	switch {
	case strings.HasPrefix(p, "test/browser/"):
		return true
	case strings.HasSuffix(p, "_test.go"):
		return false
	}
	for _, dir := range []string{"internal/", "cmd/", "test/fakes/"} {
		if strings.HasPrefix(p, dir) {
			return true
		}
	}
	return p == "go.mod" || p == "go.sum"
}

// docOnly is documentation no test reads: docs/scheme.md and the protocol
// schema are read by Go tests, so they are not.
func docOnly(p string) bool {
	switch {
	case p == "docs/scheme.md" || p == "docs/protocol.schema.json":
		return false
	case strings.HasPrefix(p, "docs/"):
		return true
	default:
		return !strings.Contains(p, "/") && strings.HasSuffix(p, ".md")
	}
}

// browserOnly is the browser checks' own JavaScript and settings, which
// only the browser checks run.
func browserOnly(p string) bool {
	return strings.HasPrefix(p, "test/browser/") && !strings.HasSuffix(p, ".go")
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func gofmtCheck(files []string) result {
	out, err := exec.Command("gofmt", append([]string{"-l"}, files...)...).CombinedOutput()
	switch {
	case err != nil:
		return result{status: "FAIL", name: "gofmt", detail: fmt.Sprintf("gofmt: %v", err), tail: string(out)}
	case strings.TrimSpace(string(out)) != "":
		return result{status: "FAIL", name: "gofmt", detail: "not formatted: " + strings.Join(strings.Fields(string(out)), " ")}
	}
	return result{status: "PASS", name: "gofmt", detail: fmt.Sprintf("%d changed Go files", len(files))}
}

// nodeCheck only shows the console's JavaScript parses; the browser checks
// show it works.
func nodeCheck(files []string) result {
	if _, err := exec.LookPath("node"); err != nil {
		return result{status: "FAIL", name: "node --check", detail: "node is not installed, so the console's JavaScript cannot be checked here"}
	}
	var bad []string
	var tail strings.Builder
	for _, f := range files {
		if out, err := exec.Command("node", "--check", f).CombinedOutput(); err != nil {
			bad = append(bad, f)
			tail.Write(out)
		}
	}
	if len(bad) > 0 {
		return result{status: "FAIL", name: "node --check", detail: "does not parse: " + strings.Join(bad, " "), tail: tail.String()}
	}
	return result{status: "PASS", name: "node --check", detail: fmt.Sprintf("%d changed scripts parse", len(files))}
}

// inventoryScript writes the books' source inventory, catalogue.tex, and
// with --check says whether it is stale (AGENTS.md, ADR 42).
const inventoryScript = "docs/book/source/generate.py"

// bookInventory runs the catalogue's check (generate.py --check, which
// AGENTS.md asks for after a Go or Scheme change) on the tree as a commit of
// it would hold it. The generator lists the files git tracks, so a file the
// change adds and nobody added to git is missing from an inventory just
// regenerated, and generate.py --check passes: on commit-bench (3422faf1)
// a new Scheme scenario was. The check runs on a copy of git's index with
// such files added and the deleted ones removed; git's own index is left
// as it was.
func bookInventory(logs string) result {
	const name = "book catalogue"
	if _, err := exec.LookPath("python3"); err != nil {
		return result{status: "FAIL", name: name, detail: "python3 is not installed, so the books' source inventory cannot be checked here"}
	}
	dir, err := os.MkdirTemp("", "nodwyn-validate-index-")
	if err != nil {
		return result{status: "FAIL", name: name, detail: err.Error()}
	}
	defer os.RemoveAll(dir)
	index := filepath.Join(dir, "index")
	added, removed, err := treeIndex(index)
	if err != nil {
		return result{status: "FAIL", name: name, detail: "copying git's index: " + err.Error()}
	}
	// env gives the generator's git ls-files the copy, and nothing else.
	r := step(name, "generate.py --check, counting files git does not track yet", logs, time.Minute,
		"env", "GIT_INDEX_FILE="+index, "python3", inventoryScript, "--check")
	if r.status != "FAIL" || !strings.Contains(r.tail, "Source catalogue is stale") {
		return r
	}
	r.detail = "docs/book/source/catalogue.tex is stale: "
	var first []string
	if len(added) > 0 {
		first = append(first, "git add -N "+strings.Join(added, " "))
	}
	if len(removed) > 0 {
		first = append(first, "git rm --cached "+strings.Join(removed, " "))
	}
	if len(first) > 0 {
		r.detail += "the generator counts only the files git tracks, so " + strings.Join(first, " and ") + ", then "
	}
	r.detail += "run python3 docs/book/source/generate.py after your last Go or Scheme edit, and keep catalogue.tex with the change"
	r.tail = ""
	return r
}

// treeIndex writes to index a copy of git's index with the Go and Scheme
// files the tree has and git does not track added (added), and those git
// tracks and the tree no longer has removed (removed). update-index
// --info-only writes no object, where git add -N writes the empty blob, so
// the check leaves nothing in the repository.
func treeIndex(index string) (added, removed []string, err error) {
	own, err := git("rev-parse", "--git-path", "index")
	if err != nil {
		return nil, nil, err
	}
	switch raw, err := os.ReadFile(own); {
	case err == nil:
		if err := os.WriteFile(index, raw, 0o600); err != nil {
			return nil, nil, err
		}
	case !errors.Is(err, os.ErrNotExist):
		return nil, nil, err
	}
	out, err := git("ls-files", "-z", "--others", "--deleted", "--exclude-standard", "--", "*.go", "*.scm")
	if err != nil {
		return nil, nil, err
	}
	var paths []string
	for _, p := range strings.Split(out, "\x00") {
		if p == "" {
			continue
		}
		paths = append(paths, p)
		if fileExists(p) {
			added = append(added, p)
		} else {
			removed = append(removed, p)
		}
	}
	if len(paths) == 0 {
		return nil, nil, nil
	}
	cmd := exec.Command("git", "update-index", "--add", "--remove", "--info-only", "-z", "--stdin")
	cmd.Env = append(os.Environ(), "GIT_INDEX_FILE="+index)
	cmd.Stdin = strings.NewReader(strings.Join(paths, "\x00") + "\x00")
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, nil, fmt.Errorf("git update-index: %v %s", err, strings.TrimSpace(string(out)))
	}
	return added, removed, nil
}

// docRules are the house rules a command cannot run but a diff can show
// (AGENTS.md): what a prompt change, a wire change, a new primitive or new
// code must bring with it.
func docRules(base string, changed []string) []result {
	var out []result
	has := func(p string) bool { return slices.Contains(changed, p) }
	if has("internal/loop/prompts.scm") && !has("docs/adr/0032-working-habits.md") {
		out = append(out, result{status: "WARN", name: "prompts", detail: "internal/loop/prompts.scm changed without an ADR 32 amendment: a working rule you added says there which failure it comes from"})
	}
	if has("docs/protocol.schema.json") && !has("docs/protocol.md") {
		out = append(out, result{status: "WARN", name: "protocol", detail: "the wire schema changed and docs/protocol.md did not: new methods and fields are described there"})
	}
	out = append(out, primitiveTables(base, changed)...)
	code, tests, console, consoleCheck := false, false, false, false
	for _, p := range changed {
		console = console || (strings.HasPrefix(p, "internal/frontend/web/assets/") && fileExists(p))
		consoleCheck = consoleCheck || strings.HasPrefix(p, "test/browser/checks/")
		switch {
		case strings.HasSuffix(p, "_test.go") || strings.HasPrefix(p, "test/browser/checks/") || strings.HasPrefix(p, "test/exfil/"):
			tests = true
		case (strings.HasPrefix(p, "internal/") || strings.HasPrefix(p, "cmd/")) && (strings.HasSuffix(p, ".go") || strings.HasSuffix(p, ".scm") || strings.HasSuffix(p, ".js")):
			code = true
		}
	}
	if code && !tests {
		out = append(out, result{status: "WARN", name: "tests", detail: "code changed and no test did: say in your answer what checked the change"})
	}
	if console && !consoleCheck {
		out = append(out, result{status: "WARN", name: "browser check", detail: "the console changed and no check in test/browser/checks did: the check performs each new action and checks its effect (AGENTS.md), and a Go test did not run the console"})
	}
	out = append(out, weakened(base, changed)...)
	return append(out, uncheckedSetup(base, changed)...)
}

// primitiveTables fails when a primitive the change adds is missing from
// the tables in the book and the expert guide; docs/scheme.md is checked by
// TestSessionPrimitivesAreDocumented.
func primitiveTables(base string, changed []string) []result {
	names, err := newPrimitives(base, changed)
	if err != nil {
		return []result{{status: "FAIL", name: "primitive tables", detail: err.Error()}}
	}
	if len(names) == 0 {
		return nil
	}
	var missing []string
	for _, table := range []string{"docs/book/parts/scheme.tex", "docs/expert/parts/09-primitives.tex"} {
		raw, err := os.ReadFile(table)
		if err != nil {
			return []result{{status: "FAIL", name: "primitive tables", detail: err.Error()}}
		}
		for _, n := range names {
			listed := regexp.MustCompile(`\\code\{\(?` + regexp.QuoteMeta(n) + `[ )},]`)
			if !listed.Match(raw) {
				missing = append(missing, n+" in "+table)
			}
		}
	}
	if len(missing) > 0 {
		return []result{{status: "FAIL", name: "primitive tables", detail: "new primitives missing: " + strings.Join(missing, "; ")}}
	}
	return []result{{status: "PASS", name: "primitive tables", detail: "listed: " + strings.Join(names, " ")}}
}
