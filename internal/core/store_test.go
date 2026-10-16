package core

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/log"
)

func TestResumeRedactsLegacyHistory(t *testing.T) {
	t.Parallel()
	o := diskOptions(t)
	path := log.SessionPath(o.Paths.SessionsDir, "legacy")
	w, err := log.Create(path, "legacy")
	if err != nil {
		t.Fatal(err)
	}
	// Initialization may have logged guard activity before session.start.
	w.Append(log.KindNote, log.Note{Text: "initialization"})
	w.Append(log.KindSessionStart, log.SessionStart{Repo: o.Repo})
	const canary = "sk-ant-abcdefgh1234"
	w.Append(log.KindUserMsg, log.UserMsg{Text: "legacy-placeholder"})
	w.Close()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.ReplaceAll(string(raw), "legacy-placeholder", canary)), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := Resume(o, "legacy")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, ev := range s.EventsSoFar() {
		if strings.Contains(string(ev.Payload), canary) {
			t.Fatal("legacy secret entered history")
		}
	}
	info, err := (Store{Dir: o.Paths.SessionsDir}).Info("legacy")
	if err != nil || strings.Contains(info.FirstMessage, canary) {
		t.Fatalf("metadata: %+v %v", info, err)
	}
	original, _ := os.ReadFile(path)
	if !strings.Contains(string(original), canary) {
		t.Fatal("original history was rewritten")
	}
}

func TestStoreRejectsInvalidLogs(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"missing", "corrupt", "mixed", "symlink", "no-start"} {
		t.Run(kind, func(t *testing.T) {
			o := diskOptions(t)
			os.MkdirAll(o.Paths.SessionsDir, 0700)
			path := log.SessionPath(o.Paths.SessionsDir, "bad")
			switch kind {
			case "corrupt":
				os.WriteFile(path, []byte("not json\n"), 0600)
			case "symlink":
				target := filepath.Join(t.TempDir(), "target")
				os.WriteFile(target, nil, 0600)
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			case "mixed", "no-start":
				id := "bad"
				if kind == "mixed" {
					id = "other"
				}
				w, err := log.Create(path, id)
				if err != nil {
					t.Fatal(err)
				}
				if kind != "no-start" {
					w.Append(log.KindSessionStart, log.SessionStart{Repo: o.Repo})
				} else {
					w.Append(log.KindNote, log.Note{Text: "no start"})
				}
				w.Close()
			}
			if _, err := Resume(o, "bad"); err == nil {
				t.Fatal("invalid session accepted")
			}
		})
	}
}

func TestSessionTitleIsShort(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"Fix the flaky test":                  "Fix the flaky test",
		"Fix the flaky test. It fails on CI.": "Fix the flaky test",
		"What is this? Explain it":            "What is this?",
		"line one\nline two":                  "line one",
		"v1.2 is out, update the changelog":   "v1.2 is out, update the changelog",
		"The session list shows each session's first message, cut short, so two sessions that start the same way look identical. Make them easy to tell apart.": "The session list shows each session's first message, cut short, so two sessions…",
	} {
		if got := (SessionInfo{FirstMessage: in}).Title(); got != want {
			t.Errorf("Title(%q) = %q, want %q", in, got, want)
		}
	}
	// A run is read by its task, not the run prompt around it.
	run := SessionInfo{FirstMessage: "Run task:\nTidy the receipts. Then sum them.\n\nWork on one unfinished plan step", Run: &log.RunState{Task: "Tidy the receipts. Then sum them."}}
	if got := run.Title(); got != "Tidy the receipts" {
		t.Errorf("run title %q", got)
	}
}

// closedSession writes the log of a session no process has open.
func closedSession(t *testing.T, o Options, id, message string) string {
	t.Helper()
	path := log.SessionPath(o.Paths.SessionsDir, id)
	w, err := log.Create(path, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Append(log.KindSessionStart, log.SessionStart{Repo: o.Repo}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Append(log.KindUserMsg, log.UserMsg{Text: message}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// A closed session is named, archived and deleted through the store (ADR
// 38), and a process that has it open keeps it: its lock refuses all three.
func TestStoreNamesArchivesAndDeletesClosedSessions(t *testing.T) {
	t.Parallel()
	o := diskOptions(t)
	store := Store{Dir: o.Paths.SessionsDir}
	path := closedSession(t, o, "named", "Fix the flaky test. It fails on CI.")
	info := func() SessionInfo {
		t.Helper()
		i, err := store.Info("named")
		if err != nil {
			t.Fatal(err)
		}
		return i
	}
	if err := store.Rename("named", "Flaky test, second try", "tester"); err != nil {
		t.Fatal(err)
	}
	if got := info().Title(); got != "Flaky test, second try" {
		t.Fatalf("named title %q", got)
	}
	const canary = "sk-ant-abcdefgh1234"
	if err := store.Rename("named", "Deploy with "+canary, "tester"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info().Title(); strings.Contains(got, canary) || strings.Contains(string(raw), canary) || !strings.HasPrefix(got, "Deploy with ") {
		t.Fatalf("a key in a name: title %q", got)
	}
	// An empty name gives the session back the title its message gives it.
	if err := store.Rename("named", "", "tester"); err != nil {
		t.Fatal(err)
	}
	if got := info(); got.Title() != "Fix the flaky test" || got.Name != "" || got.Archived {
		t.Fatalf("name taken back: %q %+v", got.Title(), got)
	}
	if err := store.Archive("named", true, "tester"); err != nil {
		t.Fatal(err)
	}
	if !info().Archived {
		t.Fatal("archive not recorded")
	}
	held, err := log.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if store.Rename("named", "Taken", "tester") == nil || store.Archive("named", false, "tester") == nil || store.Delete("named") == nil {
		t.Fatal("changed a session another writer holds")
	}
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}
	if got := info(); got.Title() != "Fix the flaky test" || !got.Archived {
		t.Fatalf("held session changed: %+v", got)
	}
	if err := store.Delete("named"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("log still there: %v", err)
	}
	if list, err := store.List(); err != nil || len(list) != 0 {
		t.Fatalf("list after delete: %+v %v", list, err)
	}
}

// Resuming an archived session takes it out of the archive (ADR 39).
func TestResumeBringsAnArchivedSessionBack(t *testing.T) {
	t.Parallel()
	o := diskOptions(t)
	store := Store{Dir: o.Paths.SessionsDir}
	closedSession(t, o, "archived", "Tidy the receipts")
	if err := store.Archive("archived", true, "tester"); err != nil {
		t.Fatal(err)
	}
	s, err := Resume(o, "archived")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := store.Info("archived")
	if err != nil || info.Archived {
		t.Fatalf("after resume: %+v %v", info, err)
	}
}

// List gives the sessions it can read and names the logs it cannot.
func TestListNamesUnreadableLogsAndKeepsTheRest(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	w, err := log.Create(log.SessionPath(dir, "good"), "good")
	if err != nil {
		t.Fatal(err)
	}
	w.Append(log.KindSessionStart, log.SessionStart{Repo: t.TempDir()})
	w.Close()
	if err := os.WriteFile(filepath.Join(dir, "empty.jsonl"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	infos, err := Store{Dir: dir}.List()
	var bad UnreadableLogs
	if len(infos) != 1 || infos[0].ID != "good" || !errors.As(err, &bad) || len(bad) != 1 || !strings.Contains(err.Error(), "empty.jsonl") {
		t.Fatalf("List: %+v, %v", infos, err)
	}
	if infos, err := Readable(Store{Dir: dir}.List()); err != nil || len(infos) != 1 {
		t.Fatalf("Readable: %+v, %v", infos, err)
	}
}
