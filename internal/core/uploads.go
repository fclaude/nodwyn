package core

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/event"
	"github.com/fclaude/nodwyn/internal/guard"
	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/policyfile"
	"github.com/fclaude/nodwyn/internal/scheme"
)

// How an upload to Google Docs is handled is a setting the person chooses
// (docs.google_upload): allow by hand, always allow, or deny. Until they
// have, the first upload asks them which, and the answer is saved in
// config.toml for every session after it.

const uploadPrim = "google-doc-upload"

// uploadPolicy is the setting now: config.toml as it is on disk, so an
// answer given in another session or the console counts at once, or, with
// no config file, this session's own.
func (s *Session) uploadPolicy() string {
	if path := s.opts.Paths.ConfigFile; path != "" && fileExists(path) {
		if cfg, err := config.Load(path); err == nil {
			return cfg.Docs.GoogleUpload
		}
	}
	s.uploadMu.Lock()
	defer s.uploadMu.Unlock()
	if s.uploadSet {
		return s.uploadSetting
	}
	return s.opts.Config.Docs.GoogleUpload
}

// uploadChecker turns the setting into the guard's verdict on an upload:
// deny refuses it, allow lets it through without a confirm (a standing
// allow, which no security confirm yields to), and ask, or no setting yet,
// asks a person: a security confirm, which --yolo, --judge and an
// unattended run never answer yes.
type uploadChecker struct{ s *Session }

func (uploadChecker) Name() string { return "upload-setting" }

func (c uploadChecker) Check(_ context.Context, p *scheme.Prim, _ []scheme.Value) guard.Decision {
	if p.Name != uploadPrim {
		return guard.Decision{Verdict: guard.Allow}
	}
	switch c.s.uploadPolicy() {
	case config.UploadDeny:
		return guard.Decision{Verdict: guard.Deny, Reason: "uploads to Google Docs are set to deny (docs.google_upload in config.toml)"}
	case config.UploadAllow:
		return guard.Decision{Verdict: guard.Allow, Standing: true, Reason: "uploads to Google Docs are set to always allow"}
	case config.UploadAsk:
		return guard.Decision{Verdict: guard.Confirm, Security: true, Reason: askReason}
	}
	return guard.Decision{Verdict: guard.Confirm, Security: true, Reason: unsetReason}
}

const (
	unsetReason = "how uploads to Google Docs are handled is not set yet: a person chooses (docs.google_upload)"
	askReason   = "uploads to Google Docs are set to allow by hand: a person says yes to each"
)

// The answers to the upload question.
var uploadChoices = []event.ChoiceOption{
	{Key: config.UploadAsk, Label: "Allow by hand", Detail: "Ask before each upload. --yolo, --judge and unattended runs never upload."},
	{Key: config.UploadAllow, Label: "Always allow", Detail: "Upload without asking, wherever the binding table allows it. The key scan and the upload log still apply.", Local: true},
	{Key: config.UploadDeny, Label: "Deny", Detail: "Never upload; sessions from now on are not offered the tool."},
}

// chooseUploadPolicy asks the person how uploads are handled, the first
// time one needs a confirm, and saves the answer. allow approves this
// upload, deny refuses it, and ask goes on to ask about this one.
func (s *Session) chooseUploadPolicy(ctx context.Context, p guard.Prompt) (guard.Approval, error) {
	reasons := []string{
		"The model asks to send " + p.Args + " to Google Drive. How should uploads to Google Docs be handled from now on?",
		"The answer is saved in config.toml ([docs] google_upload) for every session; Settings changes it.",
	}
	for _, r := range p.Reasons {
		if r != unsetReason { // the question says it better
			reasons = append(reasons, r)
		}
	}
	key, actor, id, err := s.askChoice(ctx, "Uploads to Google Docs", reasons, uploadChoices)
	if err != nil {
		return guard.Approval{PromptID: id}, err
	}
	if err := s.setUploadPolicy(ctx, key, actor, id); err != nil {
		return guard.Approval{PromptID: id}, err
	}
	switch key {
	case config.UploadAllow:
		return guard.Approval{Allowed: true, Actor: actor, PromptID: id, Scope: "once"}, nil
	case config.UploadDeny:
		return guard.Approval{Actor: actor, PromptID: id, Scope: "once"}, nil
	}
	// Allow by hand: this upload is asked about as every later one will be.
	p.Reasons = slices.Clone(p.Reasons)
	for i, r := range p.Reasons {
		if r == unsetReason {
			p.Reasons[i] = askReason
		}
	}
	return s.confirmPrompt(ctx, p)
}

type pendingChoice struct {
	title   string
	reasons []string
	options []event.ChoiceOption
	reply   chan choiceReply
}

type choiceReply struct{ key, actor string }

// askChoice puts a question with named answers to the front-ends and waits
// for one: its key, who answered, and the prompt's id.
func (s *Session) askChoice(ctx context.Context, title string, reasons []string, options []event.ChoiceOption) (string, string, string, error) {
	s.promptMu.Lock()
	id := "p" + rand.Text()
	ch := make(chan choiceReply, 1)
	s.prompts[id] = pendingConfirmation{ctx: ctx, choice: &pendingChoice{title: title, reasons: reasons, options: options, reply: ch}}
	s.promptMu.Unlock()
	defer func() {
		s.promptMu.Lock()
		delete(s.prompts, id)
		s.promptMu.Unlock()
		s.Emit(event.Event{Kind: event.PromptClosed, Payload: event.Closed{ID: id}})
	}()
	s.Emit(event.Event{Kind: event.PromptChoice, Payload: event.Choice{ID: id, Title: title, Reasons: reasons, Choices: options}})
	select {
	case r := <-ch:
		return r.key, r.actor, id, nil
	case <-ctx.Done():
		return "", "", id, ctx.Err()
	}
}

// answerChoice delivers reply to a pending choice. The caller holds
// promptMu. An answer marked Local is refused from a paired device: it
// widens what nodwyn does without asking, which only this machine may do.
func (s *Session) answerChoice(id string, pending pendingConfirmation, reply, actor string) error {
	r := strings.ToLower(strings.TrimSpace(reply))
	for _, o := range pending.choice.options {
		if o.Key != r {
			continue
		}
		if o.Local && strings.Contains(actor, " via ") {
			return fmt.Errorf("core: %q can be chosen only on this computer, not from a paired device", o.Label)
		}
		delete(s.prompts, id)
		pending.choice.reply <- choiceReply{key: o.Key, actor: actor}
		return nil
	}
	keys := make([]string, len(pending.choice.options))
	for i, o := range pending.choice.options {
		keys[i] = o.Key
	}
	return fmt.Errorf("core: answer one of %s", strings.Join(keys, ", "))
}

// setUploadPolicy saves the upload setting: in this session, and in
// config.toml under the lock nodwyn's writers of user files share, keeping
// every other line as written. The change is recorded as setting.change.
func (s *Session) setUploadPolicy(ctx context.Context, value, actor, promptID string) error {
	s.uploadMu.Lock()
	s.uploadSetting, s.uploadSet = value, true
	s.uploadMu.Unlock()
	rec := log.SettingChange{Actor: actor, PromptID: promptID, Key: "docs.google_upload", Value: value, State: "applied"}
	if path := s.opts.Paths.ConfigFile; path != "" {
		err := policyfile.Update(ctx, path, func(raw []byte) ([]byte, error) {
			out, _, err := config.SetKey(raw, "docs", "google_upload", value)
			return out, err
		})
		if err != nil {
			rec.State, rec.Error = "failed", err.Error()
		} else if err := os.Chmod(path, 0o600); err != nil && !errors.Is(err, os.ErrNotExist) {
			rec.State, rec.Error = "failed", err.Error()
		}
	}
	if _, err := s.Record(log.KindSettingChange, rec); err != nil {
		return err
	}
	if rec.State == "failed" {
		return fmt.Errorf("core: saving the upload setting: %s", rec.Error)
	}
	return nil
}
