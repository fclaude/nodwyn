package core

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fclaude/nodwyn/internal/event"
	"github.com/fclaude/nodwyn/internal/scheme"
)

func policyDone(t *testing.T, s *Session, answer func(event.Confirm)) event.Policy {
	t.Helper()
	var result event.Policy
	timeout := time.After(5 * time.Second)
	for {
		select {
		case ev := <-s.Events():
			switch ev.Kind {
			case event.PromptConfirm:
				if answer == nil {
					t.Fatal("unexpected prompt")
				}
				answer(ev.Payload.(event.Confirm))
			case event.PolicyResult:
				result = ev.Payload.(event.Policy)
			case event.Status:
				if ev.Payload.(event.StatusInfo).State == "idle" {
					return result
				}
			}
		case <-timeout:
			s.Abort()
			t.Fatal("policy operation stalled")
		}
	}
}

func TestPolicyWritesRequireOneTimeReviewAndCurrentRevision(t *testing.T) {
	for _, name := range []string{"bindings", "approvals"} {
		for _, mode := range []string{"allow", "deny", "abort", "stale"} {
			t.Run(name+"/"+mode, func(t *testing.T) {
				o := diskOptions(t)
				o.NoConfirm = false
				s, err := New(o)
				if err != nil {
					t.Fatal(err)
				}
				defer s.Close()
				// These grants must not waive a policy confirmation.
				s.writes.Add(".")
				s.allow.Add("policy-write")
				s.mu.Lock()
				s.turnTrust = true
				s.mu.Unlock()
				id, err := s.ReadPolicy(name, "test:editor")
				if err != nil {
					t.Fatal(err)
				}
				doc := policyDone(t, s, nil)
				if doc.ID != id || doc.Err != "" || doc.Exists || doc.Revision != "missing" {
					t.Fatal(doc)
				}
				path := o.Paths.Bindings
				content := "[[bind]]\nclass='service'\ndest='example.test'\nmethods=['POST']\npositions=['body']\n"
				if name == "approvals" {
					path = filepath.Join(o.Repo, ".nodwyn", "allow.toml")
					content = "commands=['go test']\nwrites=['notes/']\n"
				}
				// An environment definition cannot replace the host primitive used by core.
				if _, err := s.Eval(context.Background(), `(define policy-write (lambda args (error "hijacked")))`); err != nil {
					t.Fatal(err)
				}
				id, err = s.WritePolicy(name, doc.Revision, content, "test:editor")
				if err != nil {
					t.Fatal(err)
				}
				prompts := 0
				result := policyDone(t, s, func(p event.Confirm) {
					prompts++
					if p.Prompt.Prim != "policy-write" || !strings.Contains(p.Prompt.Detail, "+++ "+name) {
						t.Fatal(p)
					}
					if s.CreateApproval(p.ID, "session", "test:editor") == nil || s.CreateApproval(p.ID, "turn", "test:editor") == nil {
						t.Fatal("policy received wider approval")
					}
					if mode == "stale" {
						os.MkdirAll(filepath.Dir(path), 0700)
						if err := os.WriteFile(path, []byte("# external edit\n"), 0600); err != nil {
							t.Fatal(err)
						}
					}
					if mode == "abort" {
						s.Abort()
						return
					}
					reply := "y"
					if mode == "deny" {
						reply = "n"
					}
					if err := s.AnswerFrom(p.ID, reply, "test:editor"); err != nil {
						t.Fatal(err)
					}
				})
				if prompts != 1 || result.ID != id {
					t.Fatal(prompts, result)
				}
				raw, readErr := os.ReadFile(path)
				if mode == "allow" {
					if result.Err != "" || !result.Saved || readErr != nil || string(raw) != content {
						t.Fatal(result, readErr, string(raw))
					}
					if name == "bindings" {
						if err := s.bindings.Refresh(); err != nil {
							t.Fatal(err)
						}
						if len(s.Bindings()) != 1 {
							t.Fatal("bindings did not reload")
						}
					}
					if name == "approvals" {
						if strings.Contains(strings.Join(s.Allowed(), ","), "go test") {
							t.Fatal("save silently mutated existing grants")
						}
					}
				} else {
					if result.Err == "" || result.Saved {
						t.Fatal(result)
					}
					if mode == "stale" {
						if string(raw) != "# external edit\n" {
							t.Fatal("lost external edit")
						}
					} else if !os.IsNotExist(readErr) {
						t.Fatal("denied write persisted", readErr)
					}
				}
				events, _ := json.Marshal(s.EventsSoFar())
				if !strings.Contains(string(events), "test:editor") {
					t.Fatal("missing actor audit")
				}
			})
		}
	}
}

func TestPolicyRejectsUnknownTargetsSecretsAndModelOrigin(t *testing.T) {
	o := diskOptions(t)
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.ReadPolicy("../../keychain", "test"); err == nil {
		t.Fatal("accepted arbitrary target")
	}
	if _, err := s.WritePolicy("approvals", "missing", "commands=[]\n# AKIAABCDEFGHIJKLMNOP", "test"); err == nil {
		t.Fatal("accepted secret-bearing policy")
	}
	if _, err := s.WritePolicy("approvals", "missing", "unknown=true", "test"); err == nil {
		t.Fatal("accepted unknown fields")
	}
	if _, err := s.interp.EvalString(scheme.WithOrigin(context.Background(), scheme.OriginModel), `(policy-read "bindings")`); err == nil {
		t.Fatal("model read policy editor")
	}
	if err := os.WriteFile(o.Paths.Bindings, []byte("# AKIAABCDEFGHIJKLMNOP"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadPolicy("bindings", "test"); err != nil {
		t.Fatal(err)
	}
	result := policyDone(t, s, nil)
	if result.Err == "" || result.Content != "" {
		t.Fatal(result)
	}
	raw, _ := json.Marshal(s.EventsSoFar())
	if strings.Contains(string(raw), "AKIAABCDEFGHIJKLMNOP") {
		t.Fatal("canary in history")
	}
}

func TestPolicyApprovalsCannotFollowRepositoryEscape(t *testing.T) {
	o := diskOptions(t)
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "allow.toml"), []byte("commands=[]"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(o.Repo, ".nodwyn")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadPolicy("approvals", "test"); err != nil {
		t.Fatal(err)
	}
	result := policyDone(t, s, nil)
	if result.Err == "" || result.Content != "" {
		t.Fatal("read outside repository", result)
	}
}
