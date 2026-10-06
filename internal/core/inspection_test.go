package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fclaude/nodwyn/internal/event"
)

func TestFrictionReadUsesGuardAndRedactedEvents(t *testing.T) {
	t.Parallel()
	const canary = "AKIAABCDEFGHIJKLMNOP"
	for _, mode := range []string{"allow", "deny", "confirm", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			o := diskOptions(t)
			o.NoConfirm = false
			os.MkdirAll(filepath.Join(o.Repo, "docs"), 0700)
			os.WriteFile(filepath.Join(o.Repo, "docs", "friction.md"), []byte("Friction notes\n"+canary+"\n"), 0600)
			if mode != "allow" {
				verdict := mode
				if mode == "cancel" {
					verdict = "confirm"
				}
				os.MkdirAll(filepath.Join(o.Repo, ".nodwyn"), 0700)
				os.WriteFile(filepath.Join(o.Repo, ".nodwyn", "init.scm"), []byte(`(register-check "read-test" "`+verdict+`" (lambda (name args) (equal? name "fs-read")))`), 0600)
			}
			s, err := newInitSession(o)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			id, err := s.ReadFriction("test:reader")
			if err != nil {
				t.Fatal(err)
			}
			var result event.Inspection
			deadline := time.After(5 * time.Second)
			for {
				select {
				case ev := <-s.Events():
					raw, _ := json.Marshal(ev.Payload)
					if strings.Contains(string(raw), canary) {
						t.Fatalf("canary entered event %s", ev.Kind)
					}
					switch ev.Kind {
					case event.PromptConfirm:
						if _, err := s.ReadFriction("test:other"); err == nil {
							t.Fatal("overlapping read accepted")
						}
						if mode == "cancel" {
							s.Abort()
						} else if err := s.AnswerFrom(ev.Payload.(event.Confirm).ID, "y", "test:reader"); err != nil {
							t.Fatal(err)
						}
					case event.InspectionResult:
						result = ev.Payload.(event.Inspection)
					case event.Status:
						if ev.Payload.(event.StatusInfo).State == "idle" {
							goto finished
						}
					}
				case <-deadline:
					s.Abort()
					t.Fatal("inspection did not finish")
				}
			}
		finished:
			if result.ID != id || result.Name != "friction" {
				t.Fatalf("wrong result identity: %+v", result)
			}
			if mode == "deny" || mode == "cancel" {
				if result.Err == "" || result.Text != "" {
					t.Fatal("failed read returned content")
				}
			} else if result.Err != "" || !strings.Contains(result.Text, "Friction notes") {
				t.Fatalf("missing read: %+v", result)
			}
			raw, _ := json.Marshal(s.EventsSoFar())
			if strings.Contains(string(raw), canary) {
				t.Fatal("canary entered history")
			}
			if !strings.Contains(string(raw), "test:reader") || !strings.Contains(string(raw), "guard.decision") {
				t.Fatal("missing read audit")
			}
		})
	}
}
