package core

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/fclaude/nibb/internal/detect/l1"
	"github.com/fclaude/nibb/internal/event"
	"github.com/fclaude/nibb/internal/log"
	"github.com/fclaude/nibb/internal/scheme"
)

type pendingSecret struct {
	class string
	reply chan []byte
}

// PromptAndAdd asks the user (masked input) for a secret of the given class
// and stores it in the keychain. It returns the opaque handle. The secret
// value never becomes a Scheme value (ADR 5).
func (s *Session) PromptAndAdd(ctx context.Context, class string) (*scheme.Handle, error) {
	if s.keychain == nil {
		return nil, errors.New("core: no keychain configured")
	}
	// Emit a secret prompt event and wait for the front-end to reply.
	s.promptMu.Lock()
	s.promptN++
	id := fmt.Sprintf("s%d", s.promptN)
	ch := make(chan []byte, 1)
	s.promptMu.Unlock()

	// Store the pending secret prompt so SecretReply can find it.
	s.secretsMu.Lock()
	s.secrets[id] = pendingSecret{class: class, reply: ch}
	s.secretsMu.Unlock()

	s.Emit(event.Event{Kind: event.PromptSecret, Payload: event.SecretPrompt{ID: id, Class: class}})

	select {
	case secret := <-ch:
		if len(secret) == 0 {
			return nil, errors.New("core: secret was empty or declined")
		}
		h, err := s.keychain.Add(class, secret)
		if err != nil {
			return nil, err
		}
		_, _ = s.Record(log.KindCredRegister, log.CredRegister{Class: class, Handle: h.ID})
		return h, nil
	case <-ctx.Done():
		s.secretsMu.Lock()
		delete(s.secrets, id)
		s.secretsMu.Unlock()
		return nil, ctx.Err()
	}
}

// InjectHeader implements prim.CredentialStore.
func (s *Session) InjectHeader(id, name, prefix string, req *http.Request) error {
	if s.keychain == nil {
		return errors.New("core: no keychain configured")
	}
	return s.keychain.InjectHeader(id, name, prefix, req)
}

// InjectQuery implements prim.CredentialStore.
func (s *Session) InjectQuery(id, key string, req *http.Request) error {
	if s.keychain == nil {
		return errors.New("core: no keychain configured")
	}
	return s.keychain.InjectQuery(id, key, req)
}

// InjectBody implements prim.CredentialStore.
func (s *Session) InjectBody(id string, req *http.Request) error {
	if s.keychain == nil {
		return errors.New("core: no keychain configured")
	}
	return s.keychain.InjectBody(id, req)
}

// Do implements prim.CredentialStore: sends the HTTP request. The request
// carries the turn's context; the client adds a bound on a silent server.
func (s *Session) Do(req *http.Request) (*http.Response, error) {
	return egressClient.Do(req)
}

var egressClient = &http.Client{Timeout: 2 * time.Minute}

// Redactor returns the L1 scanner used to redact Jev payloads (ADR 19).
func (s *Session) Redactor() *l1.Scanner {
	sc := l1.New()
	for _, cl := range s.opts.Config.Policy.L1.AllowClasses {
		sc.AllowClass(cl)
	}
	for _, v := range s.opts.Config.Policy.L1.AllowValues {
		sc.AllowValue(v)
	}
	return sc
}

// SecretReply delivers a masked secret to a pending PromptAndAdd.
func (s *Session) SecretReply(id string, secret []byte) error {
	s.secretsMu.Lock()
	pending, ok := s.secrets[id]
	if ok {
		delete(s.secrets, id)
	}
	s.secretsMu.Unlock()
	if !ok {
		return fmt.Errorf("core: no pending secret prompt %q", id)
	}
	pending.reply <- secret
	return nil
}
