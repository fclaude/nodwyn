// Egress primitives (P3): http-request, credential-register and credential.
//
// Both are guarded before their handler runs. http-request is an Egress
// prim, so the E3 checker (guard.Egress) looks up the credential slot
// against the binding table first; the handler only does the request the
// guard allowed, injecting the secret at the bound position.
// credential-register is not egress (no data leaves the machine); the core
// implements its prompt and storage.
//
// The handlers do not touch the keychain or the front-end directly: they
// call the CredentialStore interface, which the core implements. That keeps
// the secret's lifecycle (open, prompt, add, inject, log) in one place —
// the core session — and lets these handlers stay thin adapters.
package prim

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/fclaude/nodwyn/internal/binding"
	"github.com/fclaude/nodwyn/internal/scheme"
)

// CredentialStore is what the egress primitives need from the core session.
// The core implements it: it owns the keychain, the masked prompt, and the
// log. The interface keeps prim decoupled from core (no import cycle).
type CredentialStore interface {
	// PromptAndAdd asks the user (masked input) for a secret of class,
	// stores it in the keychain, logs cred.register, and returns the handle.
	// The secret value never becomes a Scheme value (ADR 5).
	PromptAndAdd(ctx context.Context, class string) (*scheme.Handle, error)
	// HandleForClass returns the handle of a stored credential of class, or
	// nil if none is stored.
	HandleForClass(class string) *scheme.Handle
	// InjectHeader sets the request header name to prefix + the credential
	// value ("Bearer " or "" — from the binding position).
	InjectHeader(id, name, prefix string, req *http.Request) error
	// InjectQuery sets the credential id as a URL query parameter.
	InjectQuery(id, key string, req *http.Request) error
	// InjectBody makes the credential id the request body.
	InjectBody(id string, req *http.Request) error
	// Do sends the request.
	Do(req *http.Request) (*http.Response, error)
}

// httpRequest builds the http-request prim.
func httpRequest(cs CredentialStore) *scheme.Prim {
	return &scheme.Prim{
		Name: "http-request",
		Meta: scheme.Meta{
			Doc:         "Send an HTTP request with a credential injected at a bound position. Callable from Scheme only: a handle cannot be produced from JSON, so the model reaches this through a user-registered tool.",
			Egress:      true,
			Host:        true,
			Hidden:      true,
			TaintSource: scheme.TagHTTP,
			Params: []scheme.Param{
				{Name: "cred", Type: "any", Role: "credential", Doc: "credential handle"},
				{Name: "url", Type: "string", Role: "dest", Doc: "destination URL"},
				{Name: "method", Type: "string", Role: "method", Doc: "HTTP method, e.g. POST"},
				{Name: "position", Type: "string", Role: "position", Doc: "injection position: header:NAME, header:NAME:SCHEME (e.g. header:Authorization:Bearer), query:NAME or body"},
				{Name: "headers", Type: "list", Role: "payload", Doc: "list of (name value) pairs, or ()"},
				{Name: "body", Type: "string", Role: "payload", Doc: "request body, or empty string"},
			},
		},
		Handler: func(c *scheme.Call) (scheme.Value, error) {
			return doHTTPRequest(cs, c)
		},
	}
}

func doHTTPRequest(cs CredentialStore, c *scheme.Call) (scheme.Value, error) {
	if c.Args[0].Kind != scheme.KHandle {
		return scheme.Void, fmt.Errorf("http-request: arg 1 must be a credential handle, got %s", c.Args[0].Kind)
	}
	cred := c.Args[0].H

	u, err := c.String(1)
	if err != nil {
		return scheme.Void, err
	}
	if _, err := url.Parse(u); err != nil {
		return scheme.Void, fmt.Errorf("http-request: invalid url: %w", err)
	}

	method, err := c.String(2)
	if err != nil {
		return scheme.Void, err
	}
	method = strings.ToUpper(method)

	position, err := c.String(3)
	if err != nil {
		return scheme.Void, err
	}
	where, name, prefix := binding.Position(position)

	hdrVals, _ := c.List(4)
	hdr := make(map[string][]string)
	for _, pair := range hdrVals {
		items, ok := pair.Slice()
		if !ok || len(items) != 2 {
			continue
		}
		if items[0].Kind != scheme.KString || items[1].Kind != scheme.KString {
			continue
		}
		key := http.CanonicalHeaderKey(items[0].S)
		hdr[key] = append(hdr[key], items[1].S)
	}

	body, err := c.String(5)
	if err != nil {
		return scheme.Void, err
	}

	var reqBody io.Reader
	if body != "" {
		reqBody = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(c.Ctx, method, u, reqBody)
	if err != nil {
		return scheme.Void, fmt.Errorf("http-request: %w", err)
	}
	for k, vs := range hdr {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}

	// Inject the credential at the bound position. The guard already verified
	// the position is bound for this class; the handler only does the write.
	switch where {
	case "header":
		if err := cs.InjectHeader(cred.ID, name, prefix, req); err != nil {
			return scheme.Void, fmt.Errorf("http-request: inject header: %w", err)
		}
	case "query":
		if err := cs.InjectQuery(cred.ID, name, req); err != nil {
			return scheme.Void, fmt.Errorf("http-request: inject query: %w", err)
		}
	case "body":
		if err := cs.InjectBody(cred.ID, req); err != nil {
			return scheme.Void, fmt.Errorf("http-request: inject body: %w", err)
		}
	default:
		return scheme.Void, fmt.Errorf("http-request: unknown position kind %q", where)
	}

	resp, err := cs.Do(req)
	if err != nil {
		return scheme.Void, fmt.Errorf("http-request: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return scheme.Void, fmt.Errorf("http-request: read body: %w", err)
	}

	// Same shape as shell-exec: an alist with string keys.
	return scheme.List(
		scheme.Cons(scheme.Str("status"), scheme.Int(int64(resp.StatusCode))),
		scheme.Cons(scheme.Str("body"), scheme.Str(string(respBody))),
	), nil
}

// credentialRegister builds the credential-register prim. It is Hidden (a
// user action, never a model tool). It is not Egress: no data leaves the
// machine. The guard confirms it; the core's confirmer performs the masked
// input and keychain add; the handler then returns the stored handle.
func credentialRegister(cs CredentialStore) *scheme.Prim {
	return &scheme.Prim{
		Name: "credential-register",
		Meta: scheme.Meta{
			Doc:    "Register a credential: masked input, stored in the keychain, returns the handle.",
			Host:   true,
			Hidden: true,
			Params: []scheme.Param{
				{Name: "class", Type: "string", Doc: "secret class, e.g. openai-key"},
			},
		},
		Handler: func(c *scheme.Call) (scheme.Value, error) {
			return doCredentialRegister(cs, c)
		},
	}
}

// credential builds the credential prim: the handle of a key already in the
// keychain, so Scheme can reach a keyed service (a judge, a model API)
// without asking the user for the key again. A handle is opaque and every
// use of it is checked against the binding table, so looking one up grants
// nothing. cs is nil when no keychain is open; then there is never a handle.
func credential(cs CredentialStore) *scheme.Prim {
	return &scheme.Prim{
		Name: "credential",
		Meta: scheme.Meta{
			Doc:    "Return the handle of a stored credential of class, or #f if none is stored. Not callable from model-originated code.",
			Hidden: true,
			Params: []scheme.Param{
				{Name: "class", Type: "string", Doc: "secret class, e.g. jev-key"},
			},
		},
		Handler: func(c *scheme.Call) (scheme.Value, error) {
			// The model has no use for a handle of its own: its tools are
			// user-registered, and they look up the handles they need.
			if c.Origin == scheme.OriginModel {
				return scheme.Void, fmt.Errorf("credential: not allowed from a model-originated call")
			}
			class, err := c.String(0)
			if err != nil {
				return scheme.Void, err
			}
			if cs == nil {
				return scheme.False, nil
			}
			if h := cs.HandleForClass(class); h != nil {
				return scheme.HandleValue(h), nil
			}
			return scheme.False, nil
		},
	}
}

func doCredentialRegister(cs CredentialStore, c *scheme.Call) (scheme.Value, error) {
	// Only the user may start a registration. A model-originated call --
	// possible through a user tool that wraps this primitive -- or the
	// loop's own strategy would put a "please enter your key" prompt in
	// front of the user mid-turn.
	if c.Origin != scheme.OriginUser {
		return scheme.Void, fmt.Errorf("credential-register: only you can register a key (:eval or init.scm), not %s-originated code", c.Origin)
	}
	class, err := c.String(0)
	if err != nil {
		return scheme.Void, err
	}
	if class == "" {
		return scheme.Void, fmt.Errorf("credential-register: class must be a non-empty string")
	}
	// PromptAndAdd prompts the user (masked input), stores the secret in the
	// keychain, logs cred.register, and returns the handle.
	h, err := cs.PromptAndAdd(c.Ctx, class)
	if err != nil {
		return scheme.Void, fmt.Errorf("credential-register: %w", err)
	}
	return scheme.HandleValue(h), nil
}
