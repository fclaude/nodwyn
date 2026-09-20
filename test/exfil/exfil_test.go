// Package exfil is the exfiltration scenario suite (plan §7): the project's
// most important test artifact, and a required gate for promotion.
//
// Each scenario under scenarios/ is a Scheme script plus an expected guard
// decision. The runner seeds a canary secret, runs the script through the
// real kernel + guard, and then asserts that the canary appears nowhere in
// the log, the context dump, captured Jev payloads or stdout.
//
// The runner itself arrives with the guard (B2); until then the suite is
// empty and this test only guarantees the wiring exists.
package exfil

import (
	"path/filepath"
	"testing"
)

func TestScenarios(t *testing.T) {
	files, err := filepath.Glob("scenarios/*.scm")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Log("no exfil scenarios yet (runner arrives in B2)")
		return
	}
	t.Fatalf("%d scenario(s) present but the runner is not implemented", len(files))
}
