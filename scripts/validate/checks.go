package main

import (
	"fmt"
	"os"
	"os/exec"
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
// 37, amended 2026-10-07). Checking that the catalogue lists every tracked
// file takes under a second, so a Go or Scheme change runs that, and a
// change to the release publisher runs its tests, which only CI ran
// (ADR 37, amended 2026-10-16).
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
		checks = append(checks, check{"book catalogue", catalogueCheck})
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

// catalogueCheck runs the book catalogue's own check, which AGENTS.md asks
// for after a change to tracked Go or Scheme files. It lists what git
// tracks, so a new file is missing from it until git knows of it.
func catalogueCheck(logs string) result {
	r := step("book catalogue", "docs/book/source/generate.py --check", logs, time.Minute, "python3", "docs/book/source/generate.py", "--check")
	if r.status == "FAIL" {
		r.detail += "; run python3 docs/book/source/generate.py, after git add -N on each new Go file, which the catalogue lists only once git tracks it"
	}
	return r
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
	return append(out, weakened(base, changed)...)
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
