package core

import (
	"strings"
	"testing"

	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

// What the loop does with the answer to the question asked as an existing
// test is edited (ADR 32, amended 2026-10-17). On commit-bench 94f5cac4
// (dsl22) the question fired three times in 16 seconds and the model went
// on: it answered with the request's rule for a skipped argument and kept the
// rewritten row `(fs-read "big.txt" 1 0)` "count must be at least 1", which
// the request never mentions. The answer has a form now, the loop reads it
// against the request (edit-support), a row with no quote that names it gets
// one firmer message, and the finish review lists what is still unsupported
// as its first point. Nothing is reverted or refused.

const (
	tableTest = "package calc\n\nvar cases = []struct{ in, want string }{\n\t{\"count 0\", \"count must be at least 1\"},\n\t{\"start 0\", \"start is a 1-based line number\"},\n}\n"
	// The rows as the model leaves them: both rewritten.
	tableTestRewritten = "package calc\n\nvar cases = []struct{ in, want string }{\n\t{\"count -1\", \"count must be at least 1\"},\n\t{\"start -1\", \"start is a 1-based line number\"},\n}\n"
	// A request that names the second row's case and not the first's, and
	// one that names both.
	tableRequest     = "Make Root treat a start of 0 as line 1.\n\nA start of 0 now reads from the first line. Everything else is unchanged."
	tableRequestBoth = tableRequest + " A count of 0 now reads the default number of lines."

	firmMessage = "No sentence you quoted from the request names the input or case of"
	firstPoint  = "no sentence you quoted from the request names their input or case"

	// The change the check tests, so that the turn has one besides the test.
	tableCode  = "package calc\n\nfunc evalOr(p string) string { return p }\n\nfunc Root(p string) string { return evalOr(p) + \"x\" }\n"
	tableCheck = "grep -q 'evalOr(p) + \"x\"' calc/calc.go\n"
)

// tableRepo is goRepo with a table of two cases that carry words to judge
// them by.
func tableRepo(t *testing.T) string {
	t.Helper()
	repo := goRepo(t)
	commitFiles(t, repo, map[string]string{"calc/calc_test.go": tableTest})
	return repo
}

// answeringTurns is a model that sets up a check, makes the change, rewrites
// both rows of the table (the question is asked after that), and goes on with
// replies.
func answeringTurns(replies ...fake.Turn) []fake.Turn {
	return append([]fake.Turn{
		write("c", "$TMPDIR/check.sh", tableCheck),
		write("w", "calc/calc.go", tableCode),
		write("t", "calc/calc_test.go", tableTestRewritten),
	}, replies...)
}

// withText is a turn that says something and calls a tool.
func withText(content string, call fake.Turn) fake.Turn {
	call.Content = content
	return call
}

// firstRequestWith is the index of the first request whose newest message
// contains want, or -1.
func firstRequestWith(r *rig, want string) int {
	for i := range r.srv.Requests {
		if strings.Contains(lastMessage(r, i), want) {
			return i
		}
	}
	return -1
}

// lastReview is the newest message of the last request, with the numbering
// of a review of several points taken off its first.
func lastReview(r *rig) string {
	msg := lastMessage(r, len(r.srv.Requests)-1)
	if head, rest, ok := strings.Cut(msg, "things:\n\n1. "); ok && strings.HasPrefix(head, "Before you finish") {
		return rest
	}
	return msg
}

// A reply that quotes a sentence of the request about a row answers it, and
// a reply that quotes one about something else does not: the count row is
// told once, the start row is answered, and the finish review's first point
// lists the count row alone, where the review has always listed both lines.
func TestAnAnswerIsReadAgainstTheRequest(t *testing.T) {
	t.Parallel()
	r := unattendedRigIn(t, tableRepo(t), answeringTurns(
		withText("`A start of 0 now reads from the first line` covers the start row; the count row follows the same rule.", shellArgs("r", "bash", "$TMPDIR/check.sh")),
		fake.Turn{Content: "I keep both."}, // ends the turn: the review
		fake.Turn{Content: "The count row stays."},
	)...)
	if e := r.turn(tableRequest); e != "" {
		t.Fatal(e)
	}
	// The question, after the test was edited.
	if msg := lastMessage(r, 3); !strings.Contains(msg, editedLines) || !strings.Contains(msg, "calc/calc_test.go:4: {\"count 0\", \"count must be at least 1\"},") {
		t.Fatalf("the question: %q", msg)
	}
	// The firmer message, once, for the count row alone.
	i := firstRequestWith(r, firmMessage)
	if i != 4 {
		t.Fatalf("the firmer message is in request %d, want 4", i)
	}
	msg := lastMessage(r, i)
	if !strings.Contains(msg, firmMessage+" this change to an existing test:") ||
		!strings.Contains(msg, "- calc/calc_test.go:4: {\"count 0\", \"count must be at least 1\"}, (the request does not mention `count 0` or `count must be at least 1`)") ||
		strings.Contains(msg, "calc_test.go:5") {
		t.Fatalf("the firmer message: %q", msg)
	}
	for _, want := range []string{"The default is to restore the old expectation and change the code to meet it", "the last commit's version of that test is what a grader runs", "This is said once."} {
		if !strings.Contains(msg, want) {
			t.Errorf("the firmer message lacks %q: %q", want, msg)
		}
	}
	if n := requestsMention(r, firmMessage); n != 1 {
		t.Errorf("the firmer message was said %d times, want once", n)
	}
	// The review: the unsupported row first, the supported one not at all.
	review := lastReview(r)
	if !strings.HasPrefix(review, "You changed these expectations of existing tests, and "+firstPoint+":\n- calc/calc_test.go:4: {\"count 0\", \"count must be at least 1\"},") ||
		strings.Contains(review, "calc_test.go:5") {
		t.Fatalf("the review: %q", review)
	}
	if !strings.Contains(review, "keep one only by quoting, in your report, the sentence of the request that names its input or case") {
		t.Errorf("the review does not say how to keep a row: %q", review)
	}
}

// A reply that quotes nothing is no answer yet: no firmer message while the
// model is working, and the finish review's first point says what is left.
// A model that restored the lines is answered without a word.
func TestAReplyThatQuotesNothingIsNoAnswerYet(t *testing.T) {
	t.Parallel()
	r := unattendedRigIn(t, tableRepo(t), answeringTurns(
		withText("Running the check.", shellArgs("r", "bash", "$TMPDIR/check.sh")),
		fake.Turn{Content: "done"}, // the review
		fake.Turn{Content: "Both rows stay."},
	)...)
	if e := r.turn(tableRequest); e != "" {
		t.Fatal(e)
	}
	if n := requestsMention(r, firmMessage); n != 0 {
		t.Errorf("the firmer message was said %d times to a reply that quoted nothing", n)
	}
	review := lastReview(r)
	if !strings.HasPrefix(review, "You changed these expectations of existing tests, and "+firstPoint+":\n- calc/calc_test.go:4:") ||
		!strings.Contains(review, "- calc/calc_test.go:5:") {
		t.Fatalf("the review: %q", review)
	}

	// Restored, a row is no change, and nothing is said of it.
	r = unattendedRigIn(t, tableRepo(t), answeringTurns(
		write("u", "calc/calc_test.go", tableTest),
		withText("Restored both rows; the request names neither.", shellArgs("r", "bash", "$TMPDIR/check.sh")),
		fake.Turn{Content: "done"},
	)...)
	if e := r.turn(tableRequest); e != "" {
		t.Fatal(e)
	}
	for _, phrase := range []string{firmMessage, firstPoint, "expectations of existing tests:\n"} {
		if n := requestsMention(r, phrase); n != 0 {
			t.Errorf("after a restore, %d requests say %q", n, phrase)
		}
	}
}

// The reply that ends the turn is read too: a sentence that supports a row
// answers it, and the review does not list it again. The row it does not
// support is listed first, with no firmer message before.
func TestTheAnswerThatEndsTheTurnIsRead(t *testing.T) {
	t.Parallel()
	r := unattendedRigIn(t, tableRepo(t), answeringTurns(
		shellArgs("r", "bash", "$TMPDIR/check.sh"),
		fake.Turn{Content: "The start row: `A start of 0 now reads from the first line`. The count row has no sentence."},
		fake.Turn{Content: "The count row stays."},
	)...)
	if e := r.turn(tableRequest); e != "" {
		t.Fatal(e)
	}
	if n := requestsMention(r, firmMessage); n != 0 {
		t.Errorf("the firmer message was said %d times to the reply that ended the turn", n)
	}
	review := lastReview(r)
	if !strings.Contains(review, firstPoint+":\n- calc/calc_test.go:4:") || strings.Contains(review, "calc_test.go:5") {
		t.Fatalf("the review: %q", review)
	}

	// Both supported: the review has nothing to say of them.
	r = unattendedRigIn(t, tableRepo(t), answeringTurns(
		shellArgs("r", "bash", "$TMPDIR/check.sh"),
		fake.Turn{Content: "`A start of 0 now reads from the first line` covers the start row, and `A count of 0 now reads the default number of lines` the count row."},
	)...)
	if e := r.turn(tableRequestBoth); e != "" {
		t.Fatal(e)
	}
	for _, phrase := range []string{firmMessage, firstPoint, "expectations of existing tests:\n"} {
		if n := requestsMention(r, phrase); n != 0 {
			t.Errorf("with both rows supported, %d requests say %q", n, phrase)
		}
	}
}

// The firmer message is said once for a row: a second reply that quotes the
// request and still does not support it is not told again.
func TestTheFirmerMessageIsSaidOnce(t *testing.T) {
	t.Parallel()
	r := unattendedRigIn(t, tableRepo(t), answeringTurns(
		withText("`Everything else is unchanged` covers both rows.", shellArgs("r", "bash", "$TMPDIR/check.sh")),
		withText("`Everything else is unchanged`, again.", shellArgs("s", "bash", "$TMPDIR/check.sh")),
		fake.Turn{Content: "done"},
		fake.Turn{Content: "Both rows stay."},
	)...)
	if e := r.turn(tableRequest); e != "" {
		t.Fatal(e)
	}
	if n := requestsMention(r, firmMessage); n != 1 {
		t.Errorf("the firmer message was said %d times, want once", n)
	}
	if msg := lastMessage(r, firstRequestWith(r, firmMessage)); !strings.Contains(msg, firmMessage+" these changes to an existing test:") || !strings.Contains(msg, "calc_test.go:4") || !strings.Contains(msg, "calc_test.go:5") {
		t.Fatalf("the firmer message: %q", msg)
	}
}
