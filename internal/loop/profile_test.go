package loop

import (
	"os"
	"regexp"
	"slices"
	"testing"
)

// The full profile (full.scm) restores the loop before the lean ladder by
// defining every switch the ladder turned on a second time. A switch added to
// the default files and not named there would leave a full session partly
// lean, which no test of the lean ladder would show, so every non-procedure
// define whose name starts lean- or is plan-minimal must be in full.scm,
// except the ones that only say how a lean prompt reads (they do nothing
// once lean-prompt is off).
func TestFullProfileTurnsOffEverySwitchTheDefaultsTurnOn(t *testing.T) {
	switchRE := regexp.MustCompile(`(?m)^\(define (lean-[a-z-]+|plan-minimal)[ )\n]`)
	inert := []string{"lean-tools", "lean-cli", "lean-cli-name"}
	var defined []string
	for _, file := range []string{"strategy.scm", "prompts.scm", "plan.scm", "planspec.scm", "compact.scm", "agents.scm", "optimize.scm", "shell.scm", "hygiene.scm", "readdef.scm", "envhint.scm", "checkloop.scm"} {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range switchRE.FindAllStringSubmatch(string(raw), -1) {
			if !slices.Contains(inert, m[1]) && !slices.Contains(defined, m[1]) {
				defined = append(defined, m[1])
			}
		}
	}
	if len(defined) == 0 {
		t.Fatal("no lean switch found in the default files")
	}
	raw, err := os.ReadFile("full.scm")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range defined {
		if !regexp.MustCompile(`(?m)^\(define ` + regexp.QuoteMeta(name) + ` (#f|0)\)$`).Match(raw) {
			t.Errorf("full.scm does not turn %s off (#f, or 0 for a number)", name)
		}
	}
	// The numbers the lean switches compute when strategy.scm loads are set
	// again, since redefining lean-quiet alone does not bring them back.
	for _, want := range []string{"(define finish-reminders 2)", "(define finish-review-limit 2)", "(define lean-empty-reminders 0)"} {
		if !regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(want) + `$`).Match(raw) {
			t.Errorf("full.scm lacks %s", want)
		}
	}
}
