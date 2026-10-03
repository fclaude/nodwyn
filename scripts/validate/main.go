// Command validate is this repository's check before work is called done
// (ADR 37). It looks at what changed since the branch left its upstream,
// runs the checks AGENTS.md asks for those files, runs each changed Go test
// against the unchanged code to see whether it tests the change, and prints
// one summary whose last line says PASS or FAIL. It exits 1 when a check
// failed.
//
//	make validate        (go run ./scripts/validate; the validate tool)
//
// It lives under scripts/, a protected path, so a session cannot weaken the
// check it is judged by without a person agreeing (ADR 17).
package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func main() {
	stopOnSignal()
	root, err := git("rev-parse", "--show-toplevel")
	if err != nil {
		fmt.Fprintln(os.Stderr, "validate:", err)
		os.Exit(2)
	}
	if err := os.Chdir(root); err != nil {
		fmt.Fprintln(os.Stderr, "validate:", err)
		os.Exit(2)
	}
	base, against, err := baseCommit()
	if err != nil {
		fmt.Fprintln(os.Stderr, "validate:", err)
		os.Exit(2)
	}
	changed, err := changedFiles(base)
	if err != nil {
		fmt.Fprintln(os.Stderr, "validate:", err)
		os.Exit(2)
	}
	if len(changed) == 0 {
		fmt.Printf("validate: nothing changed since %s (%s), so nothing to run\nvalidate: PASS\n", short(base), against)
		return
	}
	logs, err := os.MkdirTemp("", "nodwyn-validate-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "validate:", err)
		os.Exit(2)
	}
	fmt.Printf("validate: %d changed files since %s (%s); full output in %s\n", len(changed), short(base), against, logs)
	var results []result
	for _, c := range plan(changed) {
		results = append(results, c.run(logs))
	}
	results = append(results, againstBase(base, changed, logs)...)
	results = append(results, docRules(base, changed)...)
	os.Exit(summarise(os.Stdout, results))
}

// git runs git in the working directory and returns its trimmed output.
func git(args ...string) (string, error) {
	out, err := exec.Command("git", args...).Output()
	if err != nil {
		var stderr string
		if exit, ok := err.(*exec.ExitError); ok {
			stderr = strings.TrimSpace(string(exit.Stderr))
		}
		return "", fmt.Errorf("git %s: %v %s", strings.Join(args, " "), err, stderr)
	}
	return strings.TrimSpace(string(out)), nil
}

func short(commit string) string {
	if len(commit) > 12 {
		return commit[:12]
	}
	return commit
}
