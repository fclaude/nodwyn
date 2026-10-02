package core

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/fclaude/nodwyn/internal/binding"
	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/detect/l1"
	"github.com/fclaude/nodwyn/internal/event"
	"github.com/fclaude/nodwyn/internal/keychain"
	"github.com/fclaude/nodwyn/internal/llm"
	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/loop"
	"github.com/fclaude/nodwyn/internal/scheme"
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
		s.Emit(event.Event{Kind: event.PromptClosed, Payload: event.Closed{ID: id}})
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

// InjectHeader implements prim.CredentialStore. Every key the session sends
// goes through one of these three, after the use was checked against the
// binding table: an OAuth2 token is refreshed first if it is about to
// expire, and the refresh is logged.
func (s *Session) InjectHeader(id, name, prefix string, req *http.Request) error {
	if err := s.fresh(id, req); err != nil {
		return err
	}
	return s.keychain.InjectHeader(id, name, prefix, req)
}

// InjectQuery implements prim.CredentialStore.
func (s *Session) InjectQuery(id, key string, req *http.Request) error {
	if err := s.fresh(id, req); err != nil {
		return err
	}
	return s.keychain.InjectQuery(id, key, req)
}

// InjectBody implements prim.CredentialStore.
func (s *Session) InjectBody(id string, req *http.Request) error {
	if err := s.fresh(id, req); err != nil {
		return err
	}
	return s.keychain.InjectBody(id, req)
}

// fresh refreshes credential id if it is an OAuth2 token about to expire
// (ADR 24 §3), logging the refresh as cred.refresh. The use it is for was
// already checked against the binding table, which is what authorizes the
// refresh.
func (s *Session) fresh(id string, req *http.Request) error {
	return s.freshCtx(req.Context(), id)
}

// freshCtx is fresh for a use that is not a request: the environment of a
// contained MCP server, set once when it starts.
func (s *Session) freshCtx(ctx context.Context, id string) error {
	if s.keychain == nil {
		return errors.New("core: no keychain configured")
	}
	return s.keychain.Refresh(ctx, id, func(r keychain.Refreshed) {
		rec := log.CredRefresh{Class: r.Class, Handle: r.ID, Endpoint: r.Endpoint, Rotated: r.Rotated, OK: r.Err == nil}
		if r.Err != nil {
			rec.Error = r.Err.Error()
		} else if !r.Expires.IsZero() {
			rec.Expires = r.Expires.UTC().Format(time.RFC3339)
		}
		s.recordEarly(log.KindCredRefresh, rec)
	})
}

// credentialScopes returns the scopes an OAuth2 credential was granted (the
// one with handle id, or for "" the stored one of class); a static key has
// none (guard.CredentialScopes).
func (s *Session) credentialScopes(class, id string) []string {
	if s.keychain == nil {
		return nil
	}
	if id == "" {
		h := s.keychain.HandleForClass(class)
		if h == nil {
			return nil
		}
		id = h.ID
	}
	info, err := s.keychain.Info(id)
	if err != nil {
		return nil
	}
	return info.Scopes
}

// Do implements prim.CredentialStore: sends the HTTP request. The request
// carries the turn's context; the client adds a bound on a silent server.
// A redirect is an error, not followed: the binding was checked for the
// host the request went to, and the key and body would go along to the
// next one (llm.NoRedirect).
func (s *Session) Do(req *http.Request) (*http.Response, error) {
	resp, err := egressClient.Do(req)
	if err != nil {
		return nil, err
	}
	if err := llm.Redirected(resp, "keyed requests"); err != nil {
		resp.Body.Close()
		return nil, err
	}
	return resp, nil
}

var egressClient = &http.Client{Timeout: 2 * time.Minute, CheckRedirect: llm.NoRedirect}

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
		if s.keychain == nil {
			return llm.Permanent(fmt.Errorf("the %s key is in the keychain, which was locked when this task opened: unlock it, then close and resume the task", class))
		}
		h := s.HandleForClass(class)
		if h == nil {
			return llm.Permanent(fmt.Errorf("no %s credential in the keychain", class))
		}
		if err := s.bindings.Refresh(); err != nil {
			return err
		}
		host := strings.ToLower(req.URL.Hostname())
		scopes := s.credentialScopes(class, h.ID)
		bound, reason := s.bindings.Lookup(class, host, req.Method, modelKeyPosition, scopes...)
		if _, err := s.Record(log.KindEgressCheck, log.EgressCheck{Prim: "model-turn", Class: class, Host: host, Method: req.Method, Scopes: scopes, Bound: bound, Reason: reason}); err != nil {
			return err
		}
		if !bound {
			return llm.Permanent(errors.New(reason))
		}
		_, name, prefix := binding.Position(modelKeyPosition)
		if err := s.InjectHeader(h.ID, name, prefix, req); err != nil {
			return llm.Permanent(err)
		}
		return nil
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
		oai := llm.NewOpenAI(m.URL, auth)
		oai.MaxTokensField = m.OutputField
		oai.Images = m.Vision
		if m.CacheKey != config.CacheKeyOff {
			oai.CacheKey = cacheKey(s.id)
			oai.CacheKeyHeaderOnly = m.CacheKey == config.CacheKeyHeader
		}
		p = oai
	default: // "ollama", which validation guarantees for any other value
		p = llm.NewOllama(m.URL)
		if m.NumCtx == 0 {
			m.NumCtx = s.opts.Config.WindowNumCtx()
		}
	}
	return loop.Endpoint{Provider: p, Model: m.Model, Host: modelHost(m.URL), NumCtx: m.NumCtx, KeepAlive: m.KeepAlive, MaxOutput: m.MaxOutput, Temperature: m.Temperature, Seed: m.Seed,
		Rates: llm.Rates{Input: m.PriceInput, CachedInput: m.PriceCachedInput, Output: m.PriceOutput}}
}

// cacheKey is what a session's requests to a chat-completions server name
// their session by (config.Model.CacheKey): the same for every request of
// the session, resumed or not, and saying nothing else about it, not even
// when it started, which the id does.
func cacheKey(id string) string {
	sum := sha256.Sum256([]byte("nodwyn cache key\x00" + id))
	return hex.EncodeToString(sum[:16])
}

// modelHost is where requests to a model's URL go, host and port as the
// URL gives them: what an image's confirm names, and the only host its
// requests may carry it to.
func modelHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Host)
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
