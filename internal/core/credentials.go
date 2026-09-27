package core

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/fclaude/nibb/internal/binding"
	"github.com/fclaude/nibb/internal/config"
	"github.com/fclaude/nibb/internal/detect/l1"
	"github.com/fclaude/nibb/internal/event"
	"github.com/fclaude/nibb/internal/llm"
	"github.com/fclaude/nibb/internal/log"
	"github.com/fclaude/nibb/internal/loop"
	"github.com/fclaude/nibb/internal/scheme"
)

type secretReply struct {
	value []byte
	actor string
}

type pendingSecret struct {
	ctx   context.Context
	class string
	reply chan secretReply
}

// PromptAndAdd asks the user (masked input) for a secret of the given class
// and stores it in the keychain. It returns the opaque handle. The secret
// value never becomes a Scheme value (ADR 5).
func (s *Session) PromptAndAdd(ctx context.Context, class string) (*scheme.Handle, error) {
	if s.keychain == nil {
		return nil, errors.New("core: no keychain configured")
	}
	// Emit a secret prompt event and wait for the front-end to reply.
	id := "s" + rand.Text()
	ch := make(chan secretReply, 1)

	// Store the pending secret prompt so SecretReply can find it.
	s.secretsMu.Lock()
	s.secrets[id] = pendingSecret{ctx: ctx, class: class, reply: ch}
	s.secretsMu.Unlock()

	defer func() {
		s.secretsMu.Lock()
		delete(s.secrets, id)
		select {
		case unused := <-ch:
			clear(unused.value)
		default:
		}
		s.secretsMu.Unlock()
	}()
	s.Emit(event.Event{Kind: event.PromptSecret, Payload: event.SecretPrompt{ID: id, Class: class}})

	select {
	case reply := <-ch:
		secret := reply.value
		defer clear(secret)
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if len(secret) == 0 {
			return nil, errors.New("core: secret was empty or declined")
		}
		h, err := s.keychain.Add(class, secret)
		if err != nil {
			return nil, err
		}
		_, _ = s.Record(log.KindCredRegister, log.CredRegister{Class: class, Handle: h.ID, Actor: reply.actor, PromptID: id})
		return h, nil
	case <-ctx.Done():
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
	return s.SecretReplyFrom(id, secret, "")
}

// SecretReplyFrom copies the bytes into the typed secret channel, never into
// Scheme or a log. The consumer clears its transient copy after registration.
func (s *Session) SecretReplyFrom(id string, secret []byte, actor string) error {
	s.secretsMu.Lock()
	defer s.secretsMu.Unlock()
	pending, ok := s.secrets[id]
	if !ok || (pending.ctx != nil && pending.ctx.Err() != nil) {
		return fmt.Errorf("core: no pending secret prompt %q", id)
	}
	delete(s.secrets, id)
	pending.reply <- secretReply{value: append([]byte(nil), secret...), actor: actor}
	return nil
}

// modelKeyPosition is where a keyed model's key goes. A model's key must be
// bound for it like any other credential.
const modelKeyPosition = "header:Authorization:Bearer"

// authorize returns how requests to a keyed model get their key. The key is
// injected only if class is bound to the request's host for its method at
// modelKeyPosition, checked on every request so a binding change applies at
// once, and the check is logged like any egress.
func (s *Session) authorize(class string) func(*http.Request) error {
	return func(req *http.Request) error {
		h := s.HandleForClass(class)
		if h == nil {
			return llm.Permanent(fmt.Errorf("no %s credential in the keychain", class))
		}
		if err := s.bindings.Refresh(); err != nil {
			return err
		}
		host := strings.ToLower(req.URL.Hostname())
		bound, reason := s.bindings.Lookup(class, host, req.Method, modelKeyPosition)
		if _, err := s.Record(log.KindEgressCheck, log.EgressCheck{Prim: "model-turn", Class: class, Host: host, Method: req.Method, Bound: bound, Reason: reason}); err != nil {
			return err
		}
		if !bound {
			return llm.Permanent(errors.New(reason))
		}
		_, name, prefix := binding.Position(modelKeyPosition)
		return s.keychain.InjectHeader(h.ID, name, prefix, req)
	}
}

// models builds the model registry a strategy asks by name: the [control]
// model and every [models.NAME], each built the same way. The control
// model's provider may come with the options instead (tests, embedders);
// it is also the one the session reports and the planner uses.
func (s *Session) models() map[string]loop.Endpoint {
	out := make(map[string]loop.Endpoint, len(s.opts.Config.Models)+1)
	for name, m := range s.opts.Config.Models {
		out[name] = s.endpoint(m)
	}
	control := s.endpoint(s.opts.Config.Control)
	switch {
	case s.opts.Provider != nil:
		control.Provider = s.opts.Provider
	case s.opts.Config.Control.URL == "" || s.opts.Config.Control.Model == "":
		// A session opens without one, to replay or evaluate; its first
		// model request says what to add.
		control.Provider = unconfigured{llm.Permanent(config.MissingControl(s.opts.Paths.ConfigFile))}
	}
	s.opts.Provider = control.Provider
	out[loop.ControlModel] = control
	return out
}

// endpoint builds one configured model. A keyed one gets its key per
// request, after its binding is checked (authorize).
func (s *Session) endpoint(m config.Model) loop.Endpoint {
	var p llm.Provider
	switch m.Provider {
	case "openai":
		var auth func(*http.Request) error
		if m.Credential != "" {
			auth = s.authorize(m.Credential)
		}
		p = llm.NewOpenAI(m.URL, auth)
	default: // "ollama", which validation guarantees for any other value
		p = llm.NewOllama(m.URL)
	}
	return loop.Endpoint{Provider: p, Model: m.Model, NumCtx: m.NumCtx, KeepAlive: m.KeepAlive}
}

// unconfigured stands in for a [control] model config.toml does not name.
type unconfigured struct{ err error }

func (unconfigured) Name() string { return "unconfigured" }
func (u unconfigured) Chat(context.Context, llm.Request, func(llm.Delta)) (*llm.Response, error) {
	return nil, u.err
}
func (u unconfigured) Ping(context.Context) (string, error) { return "", u.err }

// HandleForClass returns the handle of a stored credential of class, or nil
// when there is none or no keychain is open (prim.CredentialStore).
func (s *Session) HandleForClass(class string) *scheme.Handle {
	if s.keychain == nil {
		return nil
	}
	return s.keychain.HandleForClass(class)
}
