package core

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"strings"

	"github.com/fclaude/nodwyn/internal/binding"
	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/keychain"
	"github.com/fclaude/nodwyn/internal/log"
)

// credentialUse checks a credential against the binding table and puts it on
// what an MCP server is sent: a remote server's requests (authorize), a
// contained server's environment (containedEnv, checkHosts). A session
// builds one from its own keychain, live table and log; the inspection from
// Connections, which has no session and no log, from the options and the
// table as it is, with record nil. They differ in those and in nothing else,
// so a change to how a credential is checked reaches both: Connections used
// to show the tools of a server that the next task refused, or the reverse.
type credentialUse struct {
	keychain *keychain.Store
	// lookup is the binding table; refresh, when set, reloads it first.
	lookup  func(class, host, method, position string, scopes ...string) (bool, string)
	refresh func() error
	// fresh refreshes an OAuth2 token about to expire (ADR 24 §3).
	fresh        func(ctx context.Context, id string) error
	insecureHTTP bool
	// record logs the checks made; nil records nothing.
	record func(kind log.Kind, payload any)
}

// scopes are what an OAuth2 credential was granted; a static key has none.
func (u credentialUse) scopes(id string) []string {
	info, err := u.keychain.Info(id)
	if err != nil {
		return nil
	}
	return info.Scopes
}

// authorize is the hook of a remote server's client: on every request the
// key is injected only if class is bound to the request's host, method and
// position.
func (u credentialUse) authorize(class, position, prim string) func(*http.Request) error {
	return func(req *http.Request) error {
		h := u.keychain.HandleForClass(class)
		if h == nil {
			return fmt.Errorf("no %s credential in the keychain", class)
		}
		if u.refresh != nil {
			if err := u.refresh(); err != nil {
				return err
			}
		}
		host := strings.ToLower(req.URL.Hostname())
		scopes := u.scopes(h.ID)
		bound, reason := u.lookup(class, host, req.Method, position, scopes...)
		if u.record != nil {
			u.record(log.KindEgressCheck, log.EgressCheck{Prim: prim, Class: class, Host: host, Method: req.Method, Scopes: scopes, Bound: bound, Reason: reason})
		}
		if !bound {
			return errors.New(reason)
		}
		*req = *req.WithContext(keychain.WithInsecureHTTP(req.Context(), u.insecureHTTP))
		if err := keychain.CheckTransport(req); err != nil {
			return err
		}
		if err := u.fresh(req.Context(), h.ID); err != nil {
			return err
		}
		where, name, prefix := binding.Position(position)
		switch where {
		case "header":
			return u.keychain.InjectHeader(h.ID, name, prefix, req)
		case "query":
			return u.keychain.InjectQuery(h.ID, name, req)
		}
		return fmt.Errorf("position %s is not supported for MCP servers", position)
	}
}

// checkHosts looks up the credential's class for every host of a contained
// server at its position, recording each lookup, and fails at the first that
// is not bound: the container could carry the key there.
func (u credentialUse) checkHosts(server config.MCPServer, id string) error {
	if u.refresh != nil {
		if err := u.refresh(); err != nil {
			return err
		}
	}
	scopes := u.scopes(id)
	for _, host := range server.NetworkHosts() {
		bound, reason := u.lookup(server.Credential, host, "", server.Position, scopes...)
		if u.record != nil {
			u.record(log.KindEgressCheck, log.EgressCheck{Prim: config.ToolName(server.Name, "*"), Class: server.Credential, Host: host, Scopes: scopes, Bound: bound, Reason: reason})
		}
		if !bound {
			return fmt.Errorf("its credential %s is not bound for %s at %s: %s", server.Credential, host, server.Position, reason)
		}
	}
	return nil
}

// containedEnv checks a contained server's credential for every host on its
// network list and returns how its value is put in the runtime's
// environment. An environment variable cannot be refreshed later: the server
// gets the token that is valid now.
func (u credentialUse) containedEnv(server config.MCPServer) (func(*exec.Cmd) error, error) {
	h := u.keychain.HandleForClass(server.Credential)
	if h == nil {
		return nil, fmt.Errorf("no %s credential in the keychain", server.Credential)
	}
	if err := u.checkHosts(server, h.ID); err != nil {
		return nil, err
	}
	name := config.EnvName(server.Position)
	return func(cmd *exec.Cmd) error {
		if err := u.fresh(context.Background(), h.ID); err != nil {
			return err
		}
		return u.keychain.InjectEnv(h.ID, name, cmd)
	}, nil
}

// credentialUse is the session's: its keychain, its live binding table, and
// the log the checks and refreshes go to (held until session.start while the
// session is built).
func (s *Session) credentialUse() credentialUse {
	return credentialUse{
		keychain: s.keychain, lookup: s.bindings.Lookup, refresh: s.bindings.Refresh, fresh: s.freshCtx,
		insecureHTTP: s.opts.Config.Policy.AllowInsecureHTTP, record: func(kind log.Kind, payload any) { s.recordEarly(kind, payload) },
	}
}

// inspectionCredentialUse is Connections': the options' keychain and the
// binding table as it is read now, with nothing recorded.
func inspectionCredentialUse(o Options) (credentialUse, error) {
	table, err := binding.Load(o.Paths.Bindings)
	if err != nil {
		return credentialUse{}, err
	}
	return credentialUse{
		keychain: o.Keychain, lookup: table.Lookup, insecureHTTP: o.Config.Policy.AllowInsecureHTTP,
		fresh: func(ctx context.Context, id string) error { return o.Keychain.Refresh(ctx, id, nil) },
	}, nil
}
