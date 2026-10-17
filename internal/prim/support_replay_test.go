package prim

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The replay of the support check over commit-bench (ADR 32, amended
// 2026-10-16): every patch of the benchmark's runs is read for the
// expectations of existing tests it removes or rewrites (change-removals'
// rows, paired with the lines that replace them), and each row is set against
// the request of its task twice. "Any sentence" is the most the request can
// do for a row: it is supported if some sentence of the request would support
// it as a quote. "Natural" is the sentence a model would most likely quote:
// the one that shares the most tokens with the row. A row that the real
// commit also changes (its test diff is read from the repository) is a
// change the request requires, so it should be supported. Set
// NODWYN_COMMIT_BENCH to the benchmark's directory (tasks.jsonl and runs/)
// and NODWYN_REPO to the repository it was made from; without them the test
// is skipped. It prints the rows it flags and the totals (go test -v).
func TestSupportReplaysCommitBench(t *testing.T) {
	root, repo := os.Getenv("NODWYN_COMMIT_BENCH"), os.Getenv("NODWYN_REPO")
	if root == "" || repo == "" {
		t.Skip("NODWYN_COMMIT_BENCH and NODWYN_REPO name the benchmark and its repository")
	}
	type task struct{ ID, Commit, Parent, Task string }
	var tasks []task
	f, err := os.Open(filepath.Join(root, "tasks.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for dec := json.NewDecoder(f); dec.More(); {
		var tk task
		if err := dec.Decode(&tk); err != nil {
			t.Fatal(err)
		}
		tasks = append(tasks, tk)
	}
	request := func(tk task) string {
		return "You are working on one task in this repository.\n\nRules:\n- Make the change the task describes, completely, with the tests and documents the repository's instructions for coding agents (AGENTS.md) ask a change to include.\n- Preserve existing behaviour outside the task.\n- Do not use the network for research.\n- Leave the finished change in the worktree and do not commit it.\n\nTask:\n" +
			tk.Task + "\n\nWhen the change is in place and verified, end your final answer with this exact line:\nNODWYN_RUN_COMPLETE\n"
	}
	byID := map[string]task{}
	real := map[string][]removedLine{} // the rows the real commit changes
	for _, tk := range tasks {
		byID[tk.ID] = tk
		out, err := exec.Command("git", "-C", repo, "diff", tk.Parent, tk.Commit, "--", "*_test.go", "*.py").Output()
		if err != nil {
			t.Fatalf("git diff %s: %v", tk.ID, err)
		}
		real[tk.ID] = removedExpectations(patchChange(string(out)))
	}
	sameRow := func(a, b removedLine) bool { return normalLine(a.path, a.text) == normalLine(b.path, b.text) }
	everySentence := func(req string) []string {
		var qs []string
		for _, s := range requestSentences(req) {
			if w := supportWords(s); len(w) >= supportQuoteWords {
				qs = append(qs, strings.Join(w, " "))
			}
		}
		return qs
	}

	patches, _ := filepath.Glob(filepath.Join(root, "runs", "*", "*", "patch.diff"))
	type tally struct{ rows, any, natural int }
	var all, real_ tally
	var named, seen int
	flagged := map[string][]string{} // task: flagged rows, once each
	perTask := map[string][3]int{}   // task: patches with rows, rows, rows flagged
	for _, p := range patches {
		tk, ok := byID[filepath.Base(filepath.Dir(p))]
		if !ok || strings.Contains(p, string(filepath.Separator)+"gold"+string(filepath.Separator)) {
			continue
		}
		seen++
		diff, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		rows := removedExpectations(patchChange(string(diff)))
		if len(rows) == 0 {
			continue
		}
		named++
		pt := perTask[tk.ID]
		pt[0]++
		req := request(tk)
		sentences := everySentence(req)
		for _, r := range rows {
			any := statusOf(r, req, sentences) != unsupported
			natural := statusOf(r, req, naturalQuote(r, req)) != unsupported
			all.rows++
			pt[1]++
			if !natural {
				pt[2]++
			}
			if any {
				all.any++
			}
			if natural {
				all.natural++
			}
			if slices.ContainsFunc(real[tk.ID], func(g removedLine) bool { return sameRow(g, r) }) {
				real_.rows++
				if any {
					real_.any++
				}
				if natural {
					real_.natural++
				}
			}
			perTask[tk.ID] = pt
			if !natural {
				label := fmt.Sprintf("%s:%d %.70s", filepath.Base(r.path), r.line, r.text)
				if !slices.Contains(flagged[tk.ID], label) {
					flagged[tk.ID] = append(flagged[tk.ID], label)
				}
				t.Logf("flagged %-8s %-45s %s", tk.ID, filepath.Base(filepath.Dir(filepath.Dir(p))), label)
			}
		}
	}
	for _, tk := range tasks {
		if pt, ok := perTask[tk.ID]; ok {
			t.Logf("task %s: %d patches name rows, %d rows, %d flagged", tk.ID, pt[0], pt[1], pt[2])
		}
	}
	t.Logf("%d attempt patches, %d with rows named, %d rows: supported by some sentence of the request %d, by the natural quote %d", seen, named, all.rows, all.any, all.natural)
	t.Logf("%d of those rows are also changed by the real commit: supported by some sentence %d, by the natural quote %d", real_.rows, real_.any, real_.natural)
	for _, tk := range tasks {
		if rows := flagged[tk.ID]; len(rows) > 0 {
			t.Logf("task %s: %d distinct rows flagged: %s", tk.ID, len(rows), strings.Join(rows, " | "))
		}
	}
	if all.rows == 0 {
		t.Fatal("no row was named in any patch: the benchmark's runs were not found")
	}
}
