package plain

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/test/fakes/must"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

// Line mode's :eval goes through the session's recorded entry, as the TUI's
// and the console's do, so the log holds the Scheme the person ran and what
// it gave (P2-041). It used to call the unrecorded Session.Eval.
func TestLineModeEvalIsRecorded(t *testing.T) {
	srv := fake.NewT(t)
	o := testOptions(t, srv.URL)
	must.WriteFile(t, filepath.Join(o.Repo, "notes.md"), []byte("# Notes\n"), 0o644)
	client, _ := testDaemon(t, o)
	id := openTask(t, client, o.Repo)
	got := runChat(t, &Chat{Client: client, ID: id, NoConfirm: true, In: strings.NewReader(":eval (+ 1 2)\n:eval (error \"boom\")\n")})
	if !strings.Contains(got, "you> 3\n") || !strings.Contains(got, "error: boom") {
		t.Fatalf("the results were not shown:\n%s", got)
	}
	logged, err := json.Marshal(history(t, client, id))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"kind":"eval.req"`, `(+ 1 2)`, `"kind":"eval.result"`} {
		if !strings.Contains(string(logged), want) {
			t.Fatalf("the log lacks %s:\n%s", want, logged)
		}
	}
}
