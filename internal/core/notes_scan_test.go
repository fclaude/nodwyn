package core

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/log"
)

// startLog writes a session log of one session.start for repo, its url
// padded as a real one's system prompt and tools made it.
func startLog(t *testing.T, dir, id, repo string, pad int) {
	t.Helper()
	raw, err := json.Marshal(log.SessionStart{Repo: repo, URL: strings.Repeat("x", pad)})
	if err != nil {
		t.Fatal(err)
	}
	line, _ := json.Marshal(log.Event{Seq: 1, TS: time.Now(), Session: id, Kind: log.KindSessionStart, Payload: raw})
	if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), append(line, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

// Notes kept under a spelling an old session recorded (a link to the folder)
// are found in its record and moved to the folder's canonical place.
func TestNotesUnderASpellingOnlyAnOldSessionRecordedAreMigrated(t *testing.T) {
	base := t.TempDir()
	repo, alias := filepath.Join(base, "repo"), filepath.Join(base, "alias")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(repo, alias); err != nil {
		t.Fatal(err)
	}
	paths := config.PathsUnder(filepath.Join(base, "config"), filepath.Join(base, "data"))
	if err := os.MkdirAll(paths.SessionsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	startLog(t, paths.SessionsDir, "20260930T090000Z-old", alias, 100)
	sum := sha256.Sum256([]byte(alias))
	old := filepath.Join(paths.DataDir, "projects", hex.EncodeToString(sum[:8]), "notes.md")
	if err := os.MkdirAll(filepath.Dir(old), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(old, []byte("old notes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := migrateNotes(paths, repo); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(notesFile(paths.DataDir, repo)); err != nil || string(got) != "old notes" {
		t.Fatalf("notes: %q %v", got, err)
	}
}

// migrateNotes opened and decoded every session log at every start and
// resume (850 ms for 1,500 logs of 60 KB), for a migration of one change
// made once. The logs are searched once for a folder; a marker says so.
func TestNotesMigrationSearchesTheSessionLogsOnce(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	paths := config.PathsUnder(filepath.Join(base, "config"), filepath.Join(base, "data"))
	if err := os.MkdirAll(paths.SessionsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 400; i++ {
		startLog(t, paths.SessionsDir, fmt.Sprintf("20260930T090000Z-%04d", i), filepath.Join(base, "other", fmt.Sprint(i)), 60<<10)
	}
	timed := func() time.Duration {
		began := time.Now()
		if err := migrateNotes(paths, repo); err != nil {
			t.Fatal(err)
		}
		return time.Since(began)
	}
	first, second := timed(), timed()
	if second*5 > first {
		t.Fatalf("the second start took %s, the first %s: the logs are searched again", second, first)
	}
}
