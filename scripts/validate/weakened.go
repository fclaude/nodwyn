package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// A change that fails a test or an exfil scenario can pass the gate by
// removing it: make accept then runs the smaller suite and is green, and
// againstBase looks only at tests that still exist. The tests, the
// scenarios and the guard they exercise are the check a change is judged
// by, so a change to them is a warning the answer must explain, in the
// spirit of scripts/ being protected (ADR 17, ADR 37 amended 2026-10-16).

// guardedDirs hold what the exfiltration suite exists to protect (AGENTS.md:
// guard, denylist, bindings, keychain, egress, redaction), and the suite.
var guardedDirs = []string{"internal/guard/", "internal/binding/", "internal/keychain/", "internal/egress/", "internal/redact/", "test/exfil/"}

// weakened names what the change removes from the checks, and the files of
// guarded code, scenarios and exfil tests it edits (new files are not
// listed: they only add).
func weakened(base string, changed []string) []result {
	if base == "" {
		return nil
	}
	var out []result
	if gone := removedChecks(base, changed); len(gone) > 0 {
		out = append(out, result{status: "WARN", name: "removed checks", detail: strings.Join(gone, ", ") + ". A removed check cannot fail: say in your answer why each is gone and what now covers it"})
	}
	var edited []string
	for _, p := range changed {
		if underAny(p, guardedDirs) && fileExists(p) && existedAt(base, p) {
			edited = append(edited, p)
		}
	}
	if len(edited) > 0 {
		out = append(out, result{status: "WARN", name: "guarded code", detail: "changed: " + strings.Join(edited, ", ") + ". These decide what the model may read, write and send: say in your answer what each change does to what is refused"})
	}
	return out
}

func underAny(p string, dirs []string) bool {
	for _, d := range dirs {
		if strings.HasPrefix(p, d) {
			return true
		}
	}
	return false
}

// existedAt is whether base had path.
func existedAt(base, path string) bool {
	_, err := git("cat-file", "-e", base+":"+path)
	return err == nil
}

// removedChecks lists the Go tests, exfil scenarios and browser checks base
// had in the changed files that the working tree lacks. A test that moved
// to another changed file is not removed.
func removedChecks(base string, changed []string) []string {
	now := map[string]bool{}
	for _, p := range changed {
		if !strings.HasSuffix(p, "_test.go") || !fileExists(p) {
			continue
		}
		if raw, err := os.ReadFile(p); err == nil {
			for _, t := range testFuncs(raw) {
				now[filepath.Dir(p)+"."+t.name] = true
			}
		}
	}
	var gone []string
	for _, p := range changed {
		switch {
		case strings.HasSuffix(p, "_test.go"):
			src, err := git("show", base+":"+p)
			if err != nil {
				continue
			}
			for _, t := range testFuncs([]byte(src)) {
				if id := filepath.Dir(p) + "." + t.name; !now[id] {
					gone = append(gone, "test "+id)
				}
			}
		case strings.HasPrefix(p, "test/exfil/scenarios/") && strings.HasSuffix(p, ".scm"),
			strings.HasPrefix(p, "test/browser/checks/") && strings.HasSuffix(p, ".js"):
			if !fileExists(p) && existedAt(base, p) {
				gone = append(gone, p)
			}
		}
	}
	sort.Strings(gone)
	return gone
}
