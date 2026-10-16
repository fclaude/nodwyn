package core

import "testing"

// A shell result reads as the model read it: the exit status and the output.
// A command that ran past its limit has a status that says so in words, with
// the output it printed before it was stopped.
func TestResultTextReadsAShellResultByItsStatus(t *testing.T) {
	for in, want := range map[string]string{
		`{"status":2,"stdout":"FAIL\n","stderr":""}`:                           "exit 2\nFAIL\n",
		`{"status":"timed out after 10m0s","stdout":"FAIL\n","stderr":"oops"}`: "timed out after 10m0s\nFAIL\n\n[stderr]\noops",
	} {
		if got := resultText(in); got != want {
			t.Errorf("%s\n got %q\nwant %q", in, got, want)
		}
	}
}
