package core

import (
	"os"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/log"
)

func TestReadLogPagesLegacyRedactionAndPartialTail(t *testing.T) {
	t.Parallel()
	o := diskOptions(t)
	path := log.SessionPath(o.Paths.SessionsDir, "page")
	w, err := log.Create(path, "page")
	if err != nil {
		t.Fatal(err)
	}
	w.Append(log.KindSessionStart, log.SessionStart{Repo: o.Repo})
	w.Append(log.KindNote, log.Note{Text: "placeholder"})
	w.Append(log.KindNote, log.Note{Text: "third"})
	w.Close()
	raw, _ := os.ReadFile(path)
	const canary = "sk-ant-abcdefgh1234"
	original := strings.ReplaceAll(string(raw), "placeholder", canary) + `{"seq":4`
	os.WriteFile(path, []byte(original), 0600)
	store := Store{Dir: o.Paths.SessionsDir}
	first, err := store.ReadLog("page", 0, 0, 1)
	if err != nil || first.Through != 3 || first.Next != 1 || !first.More || !first.Truncated {
		t.Fatalf("first: %+v %v", first, err)
	}
	next, err := store.ReadLog("page", first.Next, first.Through, 100)
	if err != nil || next.More || len(next.Events) != 2 {
		t.Fatalf("next: %+v %v", next, err)
	}
	if strings.Contains(string(next.Events[0].Payload), canary) {
		t.Fatal("legacy credential leaked")
	}
	after, _ := os.ReadFile(path)
	if string(after) != original {
		t.Fatal("log.read modified history")
	}
	for _, test := range []struct {
		id             string
		after, through uint64
		limit          int
	}{
		{"../page", 0, 0, 1}, {"page", 4, 0, 1}, {"page", 0, 4, 1}, {"page", 0, 0, -1}, {"page", 0, 0, 1001},
	} {
		if _, err := store.ReadLog(test.id, test.after, test.through, test.limit); err == nil {
			t.Fatalf("accepted bad cursor: %+v", test)
		}
	}
}
