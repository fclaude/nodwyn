package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// A failing browser check was left to the model to explain: the summary
// showed the end of the log, which held the checks that passed, and whether
// the unchanged code failed the same way took it 2.5 to 6 minutes a time in
// the local sessions of 2026-10-03 to 10-05 (20261004T032020Z-442c04d4 with
// a wrong diagnosis, cd08f00d, 40187c24). So validate names the checks that
// failed and their FAILED lines, runs each once more on the change (a check
// that passes then is flaky), and runs those that fail again on the
// unchanged code: one that fails there too is not the change's.

var checkEnd = regexp.MustCompile(`(?m)^== (\S+): (ok|FAILED.*) in [0-9.]+s$`)

// failedChecks are the checks run.sh's output says failed, in its order.
func failedChecks(out string) []string {
	var names []string
	for _, m := range checkEnd.FindAllStringSubmatch(out, -1) {
		if strings.HasPrefix(m[2], "FAILED") {
			names = append(names, m[1])
		}
	}
	return names
}

// failureLines are the lines of each named check's section that say why it
// failed: those starting FAILED, and the first line under each.
func failureLines(out string, names []string) string {
	var b strings.Builder
	section := ""
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "== ") && !strings.Contains(line, ": ") {
			section = strings.TrimPrefix(line, "== ")
			continue
		}
		if slices.Contains(names, section) && strings.HasPrefix(line, "FAILED") {
			fmt.Fprintf(&b, "%s: %s\n", section, line)
		}
	}
	return b.String()
}

// browserVerdict is the summary line for checks that failed on the change,
// failed again when run once more, and failed on the unchanged code: those
// that failed again and pass on the unchanged code are the change's (FAIL);
// any that passed when run again are flaky, and any that fail on the
// unchanged code too are not the change's (WARN, both said).
func browserVerdict(failed, again, onBase []string, why string) result {
	var own, flaky, before []string
	for _, n := range failed {
		switch {
		case !slices.Contains(again, n):
			flaky = append(flaky, n)
		case slices.Contains(onBase, n):
			before = append(before, n)
		default:
			own = append(own, n)
		}
	}
	var says []string
	if len(own) > 0 {
		says = append(says, "fail with the change and pass on the unchanged code: "+strings.Join(own, ", "))
	}
	if len(flaky) > 0 {
		says = append(says, "failed, then passed when run again (flaky): "+strings.Join(flaky, ", "))
	}
	if len(before) > 0 {
		says = append(says, "fail on the unchanged code too, so not because of the change: "+strings.Join(before, ", "))
	}
	r := result{name: "browser checks", detail: strings.Join(says, "; "), tail: why}
	if len(own) > 0 {
		r.status = "FAIL"
	} else {
		r.status = "WARN"
		r.detail += ". Say so in your answer"
	}
	return r
}

// browserChecks runs every browser check. Without Node or Playwright it
// fails rather than skips: a console change is done when it has run in a
// browser, and the summary says how to install what is missing. When checks
// fail it says which, with their FAILED lines, runs them once more, and runs
// those that fail again on the unchanged code at base.
func browserChecks(base, logs string) result {
	if _, err := exec.LookPath("node"); err != nil {
		return result{status: "FAIL", name: "browser checks", detail: "node is not installed, so the console cannot be checked here"}
	}
	if st, err := os.Stat("test/browser/node_modules/playwright"); err != nil || !st.IsDir() {
		return result{status: "FAIL", name: "browser checks", detail: "Playwright is not installed: cd test/browser && npm install && npx playwright install chromium"}
	}
	r := step("browser checks", "test/browser/run.sh, every check", logs, 30*time.Minute, "bash", "test/browser/run.sh")
	if r.status != "FAIL" {
		return r
	}
	raw, _ := os.ReadFile(r.log)
	failed := failedChecks(string(raw))
	if len(failed) == 0 {
		return r // the checks did not get to run: the log's end says why
	}
	why := failureLines(string(raw), failed)
	again, err := checksFailing(".", failed, filepath.Join(logs, "browser-checks-again.log"))
	if err != nil {
		r.detail += "; running them again: " + err.Error()
		return r
	}
	var still []string
	for _, n := range failed {
		if slices.Contains(again, n) {
			still = append(still, n)
		}
	}
	var onBase []string
	if len(still) > 0 && base != "" {
		if onBase, err = onUnchanged(base, still, logs); err != nil {
			r.detail = "fail with the change: " + strings.Join(still, ", ") + "; on the unchanged code they could not run: " + err.Error()
			r.tail = why
			return r
		}
	}
	v := browserVerdict(failed, again, onBase, why)
	v.log = r.log
	return v
}

// checksFailing runs the named checks in dir and returns those that fail.
func checksFailing(dir string, names []string, logPath string) ([]string, error) {
	_, timedOut, err := run(dir, logPath, 20*time.Minute, "bash", append([]string{"test/browser/run.sh"}, names...)...)
	switch {
	case err != nil:
		return nil, err
	case timedOut:
		return nil, fmt.Errorf("stopped after 20m (%s)", logPath)
	}
	raw, _ := os.ReadFile(logPath)
	if !checkEnd.Match(raw) {
		return nil, fmt.Errorf("no check ran (%s)", logPath)
	}
	return failedChecks(string(raw)), nil
}

// onUnchanged runs the named checks, as the unchanged code has them, on a
// copy of base with this checkout's Playwright, and returns those that fail
// there too.
func onUnchanged(base string, names []string, logs string) ([]string, error) {
	copyDir, err := os.MkdirTemp("", "nodwyn-validate-browser-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(copyDir)
	if err := extract(base, copyDir); err != nil {
		return nil, fmt.Errorf("copying the unchanged code: %w", err)
	}
	modules, err := filepath.Abs("test/browser/node_modules")
	if err != nil {
		return nil, err
	}
	if err := os.Symlink(modules, filepath.Join(copyDir, "test/browser/node_modules")); err != nil {
		return nil, err
	}
	var have []string
	for _, n := range names {
		if fileExists(filepath.Join(copyDir, "test/browser/checks", n+".js")) {
			have = append(have, n)
		}
	}
	if len(have) == 0 {
		return nil, nil // checks the change added: the unchanged code has none to fail
	}
	return checksFailing(copyDir, have, filepath.Join(logs, "browser-checks-unchanged.log"))
}
