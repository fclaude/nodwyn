//go:build live

package live

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// result is one scenario's line in the report.
type result struct {
	Name         string   `json:"name"`
	Passed       bool     `json:"passed"`
	Skipped      bool     `json:"skipped,omitempty"`
	Seconds      float64  `json:"seconds"`
	Provider     string   `json:"provider"`
	Model        string   `json:"model"`
	Requests     int      `json:"requests"`
	PromptTokens int      `json:"prompt_tokens"`
	OutputTokens int      `json:"output_tokens"`
	Outcomes     []string `json:"turn_outcomes,omitempty"`
}

// reporter writes report.json and report.md, and the scrubbed logs of the
// scenarios that failed, to a directory the runner may keep.
type reporter struct {
	dir     string
	mu      sync.Mutex
	results []result
	logs    map[string]string
}

func newReporter(dir string) *reporter { return &reporter{dir: dir, logs: map[string]string{}} }

func (r *reporter) add(res result) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.results = append(r.results, res)
}

func (r *reporter) keepLog(scenario, id, text string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.logs[filepath.Join("logs", scenario+"-"+id+".jsonl")] = text
}

func (r *reporter) write() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.results) == 0 {
		return nil
	}
	if err := os.MkdirAll(filepath.Join(r.dir, "logs"), 0o700); err != nil {
		return err
	}
	sort.Slice(r.results, func(i, j int) bool { return r.results[i].Name < r.results[j].Name })
	raw, err := json.MarshalIndent(r.results, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(r.dir, "report.json"), raw, 0o600); err != nil {
		return err
	}
	var md strings.Builder
	passed := 0
	for _, res := range r.results {
		if res.Passed && !res.Skipped {
			passed++
		}
	}
	fmt.Fprintf(&md, "# nodwyn live run\n\n%d of %d scenarios passed on %s (%s).\n\n", passed, len(r.results), r.results[0].Model, r.results[0].Provider)
	md.WriteString("| Scenario | Result | Seconds | Requests | Prompt tokens | Output tokens | Turns ended |\n|---|---|---|---|---|---|---|\n")
	for _, res := range r.results {
		state := "pass"
		switch {
		case res.Skipped:
			state = "skipped"
		case !res.Passed:
			state = "**FAIL**"
		}
		fmt.Fprintf(&md, "| %s | %s | %.0f | %d | %d | %d | %s |\n", res.Name, state, res.Seconds, res.Requests, res.PromptTokens, res.OutputTokens, strings.Join(res.Outcomes, ", "))
	}
	if err := os.WriteFile(filepath.Join(r.dir, "report.md"), []byte(scrub(md.String())), 0o600); err != nil {
		return err
	}
	for name, text := range r.logs {
		if err := os.WriteFile(filepath.Join(r.dir, name), []byte(scrub(text)), 0o600); err != nil {
			return err
		}
	}
	fmt.Println(scrub(md.String()))
	fmt.Println("report:", r.dir)
	return nil
}
