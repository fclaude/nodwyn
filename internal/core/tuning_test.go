package core

import (
	"fmt"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/config"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

// The two tables the loop ships (plan-explore-rounds, plan.scm, and
// reading-reminder-after, strategy.scm) give the families that batch their
// calls into few rounds a longer explorer, and every other model, claude-sonnet
// and gpt-6.1-sol among them, the default; qwen, read against its logs, a
// shorter one.
func TestTunedValuesFollowTheModelsFamily(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		model, family string
		explore       int
	}{
		{"test-model", "test-model", 12},
		{"gpt-6.1-sol", "gpt-6.1-sol", 12},
		{"openai/gpt-6.1-sol", "gpt-6.1-sol", 12},
		{"claude-sonnet-5-5", "claude-sonnet-5-5", 12},
		{"anthropic/claude-sonnet-5.5", "claude-sonnet-5.5", 12},
		{"gpt-6-luna", "gpt-6-luna", 16},
		{"openai/gpt-6-luna", "gpt-6-luna", 16},
		{"meta/muse-spark-1.3-contributor", "muse-spark-1.3-contributor", 16},
		{"z-ai/glm-5.3-flash", "glm-5.3-flash", 16},
		{"qwen/qwen3.8-27b", "qwen3.8-27b", 8},
		{"qwen-3.8-27b", "qwen-3.8-27b", 8},
		{"deepseek/deepseek-v4.1-flash", "deepseek-v4.1-flash", 16},
	} {
		t.Run(tc.model, func(t *testing.T) {
			t.Parallel()
			r := newRigWith(t, true, func(c *config.Config) { c.Control.Model = tc.model })
			got := r.eval(`(list (model-family-id) (tuned plan-explore-rounds) (tuned reading-reminder-after))`)
			if want := fmt.Sprintf(`("%s" %d 12)`, tc.family, tc.explore); got != want {
				t.Fatalf("family, explorer rounds and reminder for %s = %s, want %s", tc.model, got, want)
			}
		})
	}
}

// A knob is a number, which holds for every model, or a table whose first
// fitting row wins and whose default row takes the rest.
func TestTunedKnobIsANumberOrATable(t *testing.T) {
	t.Parallel()
	const table = `'(("qwen3.8-max" . 30) ("qwen" . 20) (default . 12))`
	for _, tc := range []struct{ model, src, want string }{
		{"z-ai/glm-5.3-flash", `(tuned 7)`, "7"},
		{"qwen/qwen3.8-max-0902", `(tuned ` + table + `)`, "30"},
		{"qwen/qwen3.8-27b", `(tuned ` + table + `)`, "20"},
		{"gpt-6.1-sol", `(tuned ` + table + `)`, "12"},
		{"gpt-6.1-sol", `(tuned '(("qwen" . 20)))`, "#f"},
	} {
		r := newRigWith(t, true, func(c *config.Config) { c.Control.Model = tc.model })
		if got := r.eval(tc.src); got != tc.want {
			t.Errorf("%s on %s = %s, want %s", tc.src, tc.model, got, tc.want)
		}
	}
}

// The explorer runs for the rounds its family's row gives it: a round past
// them is skipped, and the explorer is asked for its report.
func TestExplorerRoundsFollowTheModelsFamily(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		model  string
		rounds int
	}{
		{"test-model", 12},
		{"openai/gpt-6-luna", 16},
	} {
		t.Run(tc.model, func(t *testing.T) {
			t.Parallel()
			var turns []fake.Turn
			// Each round looks for something else: a call that repeats is
			// skipped on its own account.
			for i := 0; i <= tc.rounds; i++ {
				turns = append(turns, look(fmt.Sprint("e", i), "index-grep", map[string]any{"path": ".", "pattern": fmt.Sprint("word", i)}))
			}
			turns = append(turns,
				fake.Turn{Content: "main.go declares package main."}, // the explorer's report
				fake.Turn{Content: "1. Edit main.go."},               // the planner
				fake.Turn{Content: "done"}, fake.Turn{Content: "done"}, fake.Turn{Content: "done"},
			)
			r := newRigWith(t, true, func(c *config.Config) {
				c.Control.Model = tc.model
				c.Loop.MaxToolCalls = 100
			}, turns...)
			if err := r.s.SendPlanned("change main.go"); err != nil {
				t.Fatal(err)
			}
			r.drain("n")
			results := r.results()
			last, over := fmt.Sprint("e", tc.rounds-1), fmt.Sprint("e", tc.rounds)
			if got, ok := results[last]; !ok || strings.Contains(got, "out of rounds") {
				t.Errorf("round %d of %d did not run: %q", tc.rounds, tc.rounds, got)
			}
			if got := results[over]; !strings.Contains(got, "out of rounds") {
				t.Errorf("round %d, past the %d, was not skipped: %q", tc.rounds+1, tc.rounds, got)
			}
		})
	}
}

// The reminder's count is the family's too, and what it says is that count.
func TestReadingReminderFollowsTheModelsFamily(t *testing.T) {
	t.Parallel()
	const init = `(define reading-reminder-after '(("glm" . 2) (default . 3)))` + "\n"
	for _, tc := range []struct {
		model  string
		rounds int
	}{
		{"z-ai/glm-5.3-flash", 2},
		{"gpt-6.1-sol", 3},
	} {
		t.Run(tc.model, func(t *testing.T) {
			t.Parallel()
			var turns []fake.Turn
			for i := 0; i < tc.rounds; i++ {
				turns = append(turns, look(fmt.Sprint("r", i), "index-grep", map[string]any{"path": ".", "pattern": fmt.Sprint("word", i)}))
			}
			turns = append(turns, fake.Turn{Content: "There is nothing to enable yet."})
			r := strategyRigAs(t, tc.model, init, 10, turns...)
			if err := r.turn("the UI does not let me enable worktrees"); err != "" {
				t.Fatal(err)
			}
			for i := 1; i < tc.rounds; i++ {
				if got := lastMessage(r, i); strings.Contains(got, "You have only read") {
					t.Fatalf("reminded after %d rounds: %q", i, got)
				}
			}
			if got, want := lastMessage(r, tc.rounds), fmt.Sprintf("You have only read and searched for the last %d rounds.", tc.rounds); !strings.HasPrefix(got, want) {
				t.Fatalf("after %d rounds: %q, want it to start %q", tc.rounds, got, want)
			}
		})
	}
}
