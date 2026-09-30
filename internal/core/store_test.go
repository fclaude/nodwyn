package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fclaude/nibb/internal/log"
)

func TestResumeRedactsLegacyHistory(t *testing.T) {
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
}
