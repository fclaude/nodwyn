package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/event"
	"github.com/fclaude/nodwyn/internal/log"
	fakedrive "github.com/fclaude/nodwyn/test/fakes/drive"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

// answering drives a turn to idle, answering a question with choice (from
// actor) and a confirm with confirm; it returns the kinds of prompt seen.
func (r *rig) answering(choice, actor, confirm string) []string {
	r.t.Helper()
	var seen []string
	for _, ev := range r.prompting(choice, actor, confirm) {
		seen = append(seen, ev.Kind)
	}
	return seen
}

// prompting is answering, returning the prompts themselves.
func (r *rig) prompting(choice, actor, confirm string) []event.Event {
	r.t.Helper()
	var seen []event.Event
	timeout := time.After(10 * time.Second)
	for {
		select {
		case ev := <-r.s.Events():
			switch ev.Kind {
			case event.PromptChoice:
				seen = append(seen, ev)
				if err := r.s.AnswerFrom(ev.Payload.(event.Choice).ID, choice, actor); err != nil {
					r.t.Fatal(err)
				}
			case event.PromptConfirm:
				seen = append(seen, ev)
				if err := r.s.AnswerFrom(ev.Payload.(event.Confirm).ID, confirm, actor); err != nil {
					r.t.Fatal(err)
				}
			case event.Status:
				if ev.Payload.(event.StatusInfo).State == "idle" {
					return seen
				}
			}
		case <-timeout:
			r.t.Fatal("no idle after 10s")
		}
	}
}

func uploadRig(t *testing.T, setup docsSetup) (*rig, config.Paths, *fakedrive.Server) {
	t.Helper()
	drive := fakedrive.New()
	t.Cleanup(drive.Close)
	if setup.turns == nil {
		setup.turns = []fake.Turn{uploadCall("u1", "notes.txt", "Notes"), {Content: "done"}, uploadCall("u2", "notes.txt", "Notes"), {Content: "done again"}}
	}
	r, paths := docsRig(t, drive, setup)
	os.WriteFile(filepath.Join(r.repo, "notes.txt"), []byte("meeting notes\n"), 0o644)
	return r, paths, drive
}

func (r *rig) settingChanges() []log.SettingChange {
	r.t.Helper()
	var out []log.SettingChange
	for _, ev := range r.logged() {
		var c log.SettingChange
		if ev.Kind == log.KindSettingChange && ev.Decode(&c) == nil {
			out = append(out, c)
		}
	}
	return out
}

// With no setting, the first upload asks how uploads are handled; "always
// allow" sends this one without a confirm, is saved in config.toml and in
// the log, and the next upload asks nothing.
func TestFirstUploadAsksAndAlwaysAllowIsSaved(t *testing.T) {
	t.Parallel()
	r, paths, drive := uploadRig(t, docsSetup{upload: "unset"})
	r.s.Send("upload the notes")
	if seen := r.answering("allow", "terminal", "y"); strings.Join(seen, " ") != "prompt.choice" {
		t.Fatalf("prompts: %v", seen)
	}
	if drive.Requests() != 1 {
		t.Fatalf("uploads: %d", drive.Requests())
	}
	raw, _ := os.ReadFile(paths.ConfigFile)
	if !strings.Contains(string(raw), `google_upload = "allow"`) {
		t.Fatalf("config.toml:\n%s", raw)
	}
	if c := r.settingChanges(); len(c) != 1 || c[0].Key != "docs.google_upload" || c[0].Value != "allow" || c[0].State != "applied" || c[0].Actor != "terminal" || c[0].PromptID == "" {
		t.Fatalf("setting.change %+v", c)
	}
	r.s.Send("again")
	if seen := r.answering("", "terminal", "n"); len(seen) != 0 {
		t.Fatalf("an allowed upload asked: %v", seen)
	}
	if drive.Requests() != 2 {
		t.Fatalf("uploads: %d", drive.Requests())
	}
}

// "Deny" refuses this upload and every later one; "allow by hand" asks
// about this one, and each after it.
func TestFirstUploadDenyAndAllowByHand(t *testing.T) {
	t.Parallel()
	r, _, drive := uploadRig(t, docsSetup{upload: "unset"})
	r.s.Send("upload the notes")
	r.answering("deny", "terminal", "y")
	r.s.Send("again")
	if seen := r.answering("", "terminal", "y"); len(seen) != 0 || drive.Requests() != 0 {
		t.Fatalf("deny: prompts %v, uploads %d", seen, drive.Requests())
	}
	if !strings.Contains(r.logb.String(), "uploads to Google Docs are set to deny") {
		t.Fatal("the refusal does not say why")
	}

	r, _, drive = uploadRig(t, docsSetup{upload: "unset"})
	r.s.Send("upload the notes")
	seen := r.prompting("ask", "terminal", "y")
	if len(seen) != 2 || seen[1].Kind != event.PromptConfirm || drive.Requests() != 1 {
		t.Fatalf("ask: prompts %v, uploads %d", seen, drive.Requests())
	}
	// The confirm that follows says what was just chosen.
	if reasons := strings.Join(seen[1].Payload.(event.Confirm).Prompt.Reasons, "; "); !strings.Contains(reasons, "allow by hand") || strings.Contains(reasons, "not set yet") {
		t.Fatalf("confirm reasons: %s", reasons)
	}
	r.s.Send("again")
	if seen := r.answering("", "terminal", "n"); strings.Join(seen, " ") != "prompt.confirm" || drive.Requests() != 1 {
		t.Fatalf("ask again: prompts %v, uploads %d", seen, drive.Requests())
	}
}

// A paired device may not choose "always allow": it widens what nodwyn does
// without asking, which only this machine may decide.
func TestPairedDeviceCannotChooseAlwaysAllow(t *testing.T) {
	t.Parallel()
	r, _, _ := uploadRig(t, docsSetup{upload: "unset"})
	r.s.Send("upload the notes")
	timeout := time.After(10 * time.Second)
	for {
		select {
		case ev := <-r.s.Events():
			if ev.Kind != event.PromptChoice {
				continue
			}
			id := ev.Payload.(event.Choice).ID
			if err := r.s.AnswerFrom(id, "allow", "user via web:phone"); err == nil || !strings.Contains(err.Error(), "only on this computer") {
				t.Fatalf("a phone chose always allow: %v", err)
			}
			if err := r.s.AnswerFrom(id, "deny", "user via web:phone"); err != nil {
				t.Fatal(err)
			}
			r.drain("n")
			return
		case <-timeout:
			t.Fatal("no question")
		}
	}
}

// Nobody to ask: an unattended or --yolo session with no setting, or with
// allow by hand, refuses the upload without a prompt; "always allow" uploads
// even there; "deny" is not offered the tool at all.
func TestUploadSettingWhenNobodyAsks(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		upload  string
		adjust  func(*Options)
		uploads int
	}{
		{"unset", func(o *Options) { o.Unattended = true }, 0},
		{"ask", func(o *Options) { o.AutoApprove = true }, 0},
		{"allow", func(o *Options) { o.Unattended = true }, 2},
	} {
		// Each turn uploads once and changes no file, so it is reminded
		// once before it ends (an unattended or --yolo turn).
		turns := []fake.Turn{uploadCall("u1", "notes.txt", "Notes"), {Content: "done"}, {Content: "done"}, uploadCall("u2", "notes.txt", "Notes"), {Content: "done again"}, {Content: "done again"}}
		r, _, drive := uploadRig(t, docsSetup{upload: tc.upload, adjust: tc.adjust, turns: turns})
		for i, text := range []string{"upload the notes", "again"} {
			r.s.Send(text)
			if seen := r.answering("allow", "terminal", "y"); len(seen) != 0 {
				t.Fatalf("%s: asked %v", tc.upload, seen)
			}
			if want := tc.uploads * (i + 1) / 2; drive.Requests() != want {
				t.Fatalf("%s: after turn %d, uploads %d, want %d", tc.upload, i+1, drive.Requests(), want)
			}
		}
	}
	r, _, _ := uploadRig(t, docsSetup{upload: "deny", turns: []fake.Turn{{Content: "nothing"}}})
	if strings.Contains(toolNames(r.s), "google-doc-upload") {
		t.Fatal("uploads set to deny still offer the tool")
	}
}
