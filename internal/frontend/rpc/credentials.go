package rpc

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/fclaude/nodwyn/internal/binding"
	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/keychain"
	"github.com/fclaude/nodwyn/internal/scheme"
)

// The methods in this file manage the keychain's credentials without a
// terminal (ADR 5, ADR 24, ADR 30): list them with where each may be sent
// and what uses it, add and replace static keys, allow one for a host,
// authorize an OAuth2 credential with its provider, and delete or revoke
// one. No method returns a secret: values go in, never out, and the
// keychain injects them at a bound position as it always has.

type credentialsResult struct {
	Keychain    keychainStatus      `json:"keychain"`
	Credentials []credentialInfo    `json:"credentials"`
	Missing     []missingCredential `json:"missing"`
	// Authorizations are the OAuth2 authorizations this daemon started in
	// the last ten minutes, waiting or finished.
	Authorizations []authorizationInfo `json:"authorizations"`
	Providers      []providerInfo      `json:"providers"`
	BindingsFile   string              `json:"bindings_file"`
	// BindingsError is why bindings.toml does not load, when it does not:
	// the rows shown are then the ones this daemon last loaded.
	BindingsError string `json:"bindings_error,omitempty"`
}

// credentialInfo is everything that may be said about a credential.
type credentialInfo struct {
	ID          string     `json:"id"`
	Class       string     `json:"class"`
	Kind        string     `json:"kind"`
	Created     time.Time  `json:"created"`
	Provider    string     `json:"provider,omitempty"`
	Scopes      []string   `json:"scopes,omitempty"`
	Expires     *time.Time `json:"expires,omitempty"`
	Refreshable bool       `json:"refreshable,omitempty"`
	NeedsReauth bool       `json:"needs_reauth,omitempty"`
	TokenHost   string     `json:"token_host,omitempty"`
	// Shadowed means an earlier credential has the same class: a lookup by
	// class finds that one, so this one is never sent. The keychain no longer
	// stores a second of a class, but one made before that may hold them.
	Shadowed bool `json:"shadowed,omitempty"`
	// UsedBy names what is configured to send it: "the main model",
	// "model fast", "MCP server linear", "Google Docs", "the judge".
	UsedBy []string `json:"used_by"`
	// Model is whether a configured model sends it: the control model or
	// one of the models a strategy may ask for. The console's Model keys
	// panel groups by it instead of matching the UsedBy labels.
	Model bool `json:"model,omitempty"`
	// Bindings are the binding rows that name its class: where it may go.
	Bindings []bindingRow `json:"bindings"`
}

type bindingRow struct {
	Dest      string   `json:"dest"`
	Methods   []string `json:"methods"`
	Positions []string `json:"positions"`
	Scopes    []string `json:"scopes,omitempty"`
}

// missingCredential is a class something is configured to send that the
// keychain does not hold: those requests fail until one is added.
type missingCredential struct {
	Class  string   `json:"class"`
	UsedBy []string `json:"used_by"`
	// Model is whether a configured model sends it. The console's Model
	// keys panel groups by it instead of matching the UsedBy labels.
	Model bool `json:"model,omitempty"`
}

type providerInfo struct {
	Name        string   `json:"name"`
	Scopes      []string `json:"scopes"`
	NeedsSecret bool     `json:"needs_secret"`
	APIHost     string   `json:"api_host"`
}

type authorizationInfo struct {
	ID       string `json:"id"`
	Class    string `json:"class"`
	Provider string `json:"provider"`
	// URL is the provider's consent page, while the authorization waits.
	URL string `json:"url,omitempty"`
	// State is "waiting", "done" or "failed".
	State string `json:"state"`
	Error string `json:"error,omitempty"`
	// Credential is the authorized credential's id, once it is done.
	Credential string `json:"credential,omitempty"`
}

type credentialAddParams struct {
	Class string `json:"class"`
	Value string `json:"value"`
	// Dest, Methods and Position, when Dest is given, allow it for that host
	// at once: one exact binding row per method.
	Dest     string   `json:"dest,omitempty"`
	Methods  []string `json:"methods,omitempty"`
	Position string   `json:"position,omitempty"`
}
type credentialReplaceParams struct {
	ID    string `json:"id"`
	Value string `json:"value"`
}
type credentialRemoveParams struct {
	ID string `json:"id"`
	// Revoke asks an OAuth2 credential's provider to revoke its tokens
	// first; without it, or for a static key, it is only deleted here.
	Revoke bool `json:"revoke,omitempty"`
}
type credentialRemoved struct {
	// Revoked reports whether the provider was asked and agreed.
	Revoked bool              `json:"revoked"`
	List    credentialsResult `json:"list"`
}
type credentialBindParams struct {
	ID       string   `json:"id"`
	Dest     string   `json:"dest"`
	Methods  []string `json:"methods"`
	Position string   `json:"position"`
}
type credentialUnbindParams struct {
	ID string `json:"id"`
	// Dest, Positions and Scopes are the row to take back, as the list shows
	// it: every method it allowed goes with it.
	Dest      string   `json:"dest"`
	Positions []string `json:"positions"`
	Scopes    []string `json:"scopes,omitempty"`
}
type credentialAuthorizeParams struct {
	// ID authorizes an existing OAuth2 credential again, with its client,
	// endpoints and scopes. Without it the rest describe a new one.
	ID           string   `json:"id,omitempty"`
	Class        string   `json:"class,omitempty"`
	Provider     string   `json:"provider,omitempty"`
	ClientID     string   `json:"client_id,omitempty"`
	ClientSecret string   `json:"client_secret,omitempty"`
	Scopes       []string `json:"scopes,omitempty"`
	AuthURL      string   `json:"auth_url,omitempty"`
	TokenURL     string   `json:"token_url,omitempty"`
	RevokeURL    string   `json:"revoke_url,omitempty"`
}

func (s *server) credentialsMethod(req request, _ *peer) (any, *rpcError) {
	switch req.Method {
	case "credentials.list":
		if err := params(req.Params, &struct{}{}); err != nil {
			return nil, err
		}
		return s.credentials(), nil
	case "credentials.authorize":
		return s.authorize(req)
	}
	store := s.options.Keychain
	if store == nil {
		return nil, failure(-32000, "Unlock or create the keychain first")
	}
	switch req.Method {
	case "credentials.add":
		var p credentialAddParams
		if err := params(req.Params, &p, "class", "value", "dest", "methods", "position"); err != nil {
			return nil, err
		}
		value := []byte(p.Value)
		defer clear(value)
		if !keychain.ValidClass(p.Class) {
			return nil, failure(-32602, keychain.BadClass)
		}
		if len(value) == 0 {
			return nil, failure(-32602, "The value is empty")
		}
		var rows []binding.Binding
		if p.Dest != "" || len(p.Methods) > 0 || p.Position != "" {
			var err *rpcError
			if rows, err = exactRows(p.Class, p.Dest, p.Methods, p.Position, nil); err != nil {
				return nil, err
			}
		}
		// A change waits for the keychain's file lock, which a token refresh
		// holds across its request to the provider: not under the lifecycle lock.
		var h *scheme.Handle
		var err error
		s.slow("", func() { h, err = store.Add(p.Class, value) })
		if err != nil {
			return nil, failure(-32000, err.Error())
		}
		if err := s.appendRows(rows); err != nil {
			return nil, failure(-32000, fmt.Sprintf("Stored %s (%s), but did not allow it for %s: %v", p.Class, h.ID, p.Dest, err))
		}
		return s.credentials(), nil
	case "credentials.replace":
		var p credentialReplaceParams
		if err := params(req.Params, &p, "id", "value"); err != nil {
			return nil, err
		}
		value := []byte(p.Value)
		defer clear(value)
		if len(value) == 0 {
			return nil, failure(-32602, "The value is empty")
		}
		var err error
		s.slow("", func() { err = store.Replace(p.ID, value) })
		if err != nil {
			return nil, failure(-32000, err.Error())
		}
		return s.credentials(), nil
	case "credentials.remove":
		var p credentialRemoveParams
		if err := params(req.Params, &p, "id", "revoke"); err != nil {
			return nil, err
		}
		in, err := store.Info(p.ID)
		if err != nil {
			return nil, failure(-32000, err.Error())
		}
		revoked := false
		if p.Revoke {
			// The provider is asked without the lifecycle lock: it may take the
			// whole timeout to answer.
			var err error
			s.slow("", func() {
				ctx, cancel := context.WithTimeout(s.base(), 15*time.Second)
				defer cancel()
				revoked, err = store.Revoke(ctx, p.ID)
			})
			if err != nil {
				// The command line's way out is --local; here it is a
				// removal without revoking.
				msg := strings.Replace(err.Error(), "delete it with --local to drop it anyway", "remove it without revoking to drop it anyway", 1)
				return nil, failure(-32000, msg)
			}
		} else {
			var err error
			s.slow("", func() { err = store.Delete(p.ID) })
			if err != nil {
				return nil, failure(-32000, err.Error())
			}
		}
		// The class's rows go with its credential, so the next credential
		// given the class does not inherit what this one was allowed; a
		// keychain made before a class named one credential may still hold
		// another, which keeps them.
		if store.HandleForClass(in.Class) == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if _, err := binding.RemoveClass(ctx, s.options.Paths.Bindings, in.Class); err != nil {
				return nil, failure(-32000, "the credential is removed, but its rows in bindings.toml are not: "+err.Error())
			}
			if err := s.reloadBindings(); err != nil {
				return nil, failure(-32000, err.Error())
			}
		}
		return credentialRemoved{Revoked: revoked, List: s.credentials()}, nil
	case "credentials.bind":
		var p credentialBindParams
		if err := params(req.Params, &p, "id", "dest", "methods", "position"); err != nil {
			return nil, err
		}
		in, err := store.Info(p.ID)
		if err != nil {
			return nil, failure(-32000, err.Error())
		}
		// An OAuth2 credential is allowed with the scopes it was granted, so
		// a broader token for the same class is not.
		rows, rpcErr := exactRows(in.Class, p.Dest, p.Methods, p.Position, in.Scopes)
		if rpcErr != nil {
			return nil, rpcErr
		}
		if err := s.appendRows(rows); err != nil {
			return nil, failure(-32000, err.Error())
		}
		return s.credentials(), nil
	case "credentials.unbind":
		var p credentialUnbindParams
		if err := params(req.Params, &p, "id", "dest", "positions", "scopes"); err != nil {
			return nil, err
		}
		in, err := store.Info(p.ID)
		if err != nil {
			return nil, failure(-32000, err.Error())
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		n, err := binding.RemoveExact(ctx, s.options.Paths.Bindings, in.Class, p.Dest, p.Positions, p.Scopes)
		if err != nil {
			return nil, failure(-32000, err.Error())
		}
		if n == 0 {
			return nil, failure(-32000, fmt.Sprintf("bindings.toml has no row allowing %s at %s as %s: it may have changed since the page was drawn", in.Class, p.Dest, strings.Join(p.Positions, ", ")))
		}
		if err := s.reloadBindings(); err != nil {
			return nil, failure(-32000, err.Error())
		}
		return s.credentials(), nil
	}
	return nil, failure(-32601, "Method not found")
}

// exactRows is one exact binding row per method, checked before anything
// is stored.
func exactRows(class, dest string, methods []string, position string, scopes []string) ([]binding.Binding, *rpcError) {
	dest = strings.ToLower(strings.TrimSpace(dest))
	if dest == "" || len(methods) == 0 || position == "" {
		return nil, failure(-32602, "Allowing a credential needs a host, at least one method and a position")
	}
	var rows []binding.Binding
	for _, method := range methods {
		row, err := binding.Exact(class, dest, strings.ToUpper(method), position, scopes...)
		if err != nil {
			return nil, failure(-32602, fmt.Sprintf("Cannot allow %s for %s %s as %s: give a host name (no scheme or path), a method such as GET or POST, and a position such as header:Authorization:Bearer", class, method, dest, position))
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// appendRows adds binding rows to bindings.toml and reloads the table new
// sessions start with; sessions already open reload it before egress.
func (s *server) appendRows(rows []binding.Binding) error {
	if len(rows) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, row := range rows {
		if err := binding.AppendExact(ctx, s.options.Paths.Bindings, row); err != nil {
			return err
		}
	}
	return s.reloadBindings()
}

// reloadBindings reads bindings.toml again for the table new sessions start
// with; sessions already open reload it before egress.
func (s *server) reloadBindings() error {
	table, err := binding.Load(s.options.Paths.Bindings)
	if err != nil {
		return err
	}
	s.options.Bindings = table
	return nil
}

// credentials describes the keychain's credentials as they are now, with
// the binding table as bindings.toml has it now (it is edited by hand and
// by sessions too).
func (s *server) credentials() credentialsResult {
	result := credentialsResult{Keychain: s.keychainStatus(), Credentials: []credentialInfo{}, Missing: []missingCredential{},
		Authorizations: s.authorizationList(), BindingsFile: s.options.Paths.Bindings}
	table, err := binding.Load(s.options.Paths.Bindings)
	if err != nil {
		table, result.BindingsError = s.options.Bindings, err.Error()
	}
	for _, name := range keychain.ProfileNames() {
		p, _ := keychain.ProfileNamed(name)
		result.Providers = append(result.Providers, providerInfo{Name: p.Name, Scopes: p.Scopes, NeedsSecret: p.NeedsSecret, APIHost: p.APIHost})
	}
	store := s.options.Keychain
	if store == nil {
		return result
	}
	users := s.credentialUsers()
	models := modelClasses(s.options.Config)
	seen := map[string]bool{}
	for _, in := range store.List() {
		info := credentialInfo{ID: in.ID, Class: in.Class, Kind: in.Kind, Created: in.Created, Provider: in.Provider, Scopes: in.Scopes,
			Refreshable: in.Refreshable, NeedsReauth: in.NeedsReauth, TokenHost: in.TokenHost, Shadowed: seen[in.Class],
			UsedBy: users.of(in.Class), Model: models[in.Class], Bindings: []bindingRow{}}
		if !in.Expires.IsZero() {
			expires := in.Expires
			info.Expires = &expires
		}
		for _, row := range table.Bind {
			if row.Class == in.Class {
				info.Bindings = append(info.Bindings, bindingRow{Dest: row.Dest, Methods: row.Methods, Positions: row.Positions, Scopes: row.Scopes})
			}
		}
		seen[in.Class] = true
		result.Credentials = append(result.Credentials, info)
	}
	for _, class := range users.required {
		if !seen[class] {
			result.Missing = append(result.Missing, missingCredential{Class: class, UsedBy: users.of(class), Model: models[class]})
		}
	}
	return result
}

// modelClasses are the credential classes the configured models send: the
// control model and one per strategy model, and the servers' keys, which a
// model sends once it names the server. The console groups its Model keys
// panel by these instead of parsing the UsedBy labels.
func modelClasses(cfg config.Config) map[string]bool {
	out := map[string]bool{}
	if cfg.Control.Credential != "" {
		out[cfg.Control.Credential] = true
	}
	for _, m := range cfg.Models {
		if m.Credential != "" {
			out[m.Credential] = true
		}
	}
	for _, s := range cfg.Servers {
		if s.Credential != "" {
			out[s.Credential] = true
		}
	}
	return out
}

// credentialUsers is what is configured to send which class.
type credentialUsers struct {
	by map[string][]string
	// required are the classes whose absence breaks something configured:
	// a model's key, an MCP server's, the judge's server's. The Google class
	// has a default whether or not anyone uses it, so it is not.
	required []string
}

func (u credentialUsers) of(class string) []string {
	if users := u.by[class]; users != nil {
		return users
	}
	return []string{}
}

func (s *server) credentialUsers() credentialUsers {
	u := credentialUsers{by: map[string][]string{}}
	use := func(class, user string, required bool) {
		if class == "" {
			return
		}
		u.by[class] = append(u.by[class], user)
		if required && !slices.Contains(u.required, class) {
			u.required = append(u.required, class)
		}
	}
	cfg := s.options.Config
	use(cfg.Control.Credential, "the main model", true)
	names := make([]string, 0, len(cfg.Models))
	for name := range cfg.Models {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		use(cfg.Models[name].Credential, "model "+name, true)
	}
	// A server's key is missed only by the models that name the server.
	for _, name := range slices.Sorted(maps.Keys(cfg.Servers)) {
		use(cfg.Servers[name].Credential, "server "+name, false)
	}
	if servers, err := config.LoadMCP(config.MCPFile(s.options.Paths)); err == nil {
		for _, server := range servers {
			use(server.Credential, "MCP server "+server.Name, true)
		}
	}
	docs := cfg.Docs
	if current, err := config.Load(s.options.Paths.ConfigFile); err == nil {
		docs = current.Docs
	}
	use(docs.GoogleCredential, "Google Docs", false)
	if srv, ok := cfg.JudgeServer(); ok {
		use(srv.Credential, "the judge", true)
	}
	return u
}

// ---------- OAuth2 authorization from the console ----------

// authorization is one OAuth2 authorization this daemon runs: the provider's
// consent page is opened by the person's browser, on this computer, and the
// provider sends it back to a listener on loopback here (ADR 24 §2). It
// outlives the method call that started it, by up to five minutes.
type authorization struct {
	info   authorizationInfo
	cancel context.CancelFunc
	ended  time.Time
}

// authorizationsKept is how long a finished authorization stays listed.
const authorizationsKept = 10 * time.Minute

// maxAuthorizations bounds the authorizations waiting at once: each holds
// a loopback listener.
const maxAuthorizations = 4

func (s *server) authorizationList() []authorizationInfo {
	out := []authorizationInfo{}
	ids := make([]string, 0, len(s.authorizations))
	for id, a := range s.authorizations {
		if !a.ended.IsZero() && time.Since(a.ended) > authorizationsKept {
			delete(s.authorizations, id)
			continue
		}
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		out = append(out, s.authorizations[id].info)
	}
	return out
}

// authorize starts an OAuth2 authorization and answers with the provider's
// consent page as soon as it exists; the page polls credentials.list for
// the outcome. Authorizing is a user action (ADR 24): this method is
// offered to the console on this computer only, never to a tool.
func (s *server) authorize(req request) (any, *rpcError) {
	var p credentialAuthorizeParams
	if err := params(req.Params, &p, "id", "class", "provider", "client_id", "client_secret", "scopes", "auth_url", "token_url", "revoke_url"); err != nil {
		return nil, err
	}
	secret := p.ClientSecret
	p.ClientSecret = ""
	store := s.options.Keychain
	if store == nil {
		return nil, failure(-32000, "Unlock or create the keychain first")
	}
	waiting := 0
	for _, a := range s.authorizations {
		if a.ended.IsZero() {
			waiting++
		}
	}
	if waiting >= maxAuthorizations {
		return nil, failure(-32000, "Finish or wait out the authorizations already open first")
	}
	var run func(ctx context.Context, open func(string) error) (string, error)
	info := authorizationInfo{ID: fmt.Sprintf("auth-%d", time.Now().UnixNano()), State: "waiting"}
	if p.ID != "" {
		if p.Class != "" || p.Provider != "" || p.ClientID != "" || secret != "" || p.Scopes != nil || p.AuthURL != "" || p.TokenURL != "" || p.RevokeURL != "" {
			return nil, failure(-32602, "Authorizing a credential again takes only its id")
		}
		in, err := store.Info(p.ID)
		if err != nil {
			return nil, failure(-32000, err.Error())
		}
		if in.Kind != keychain.KindOAuth2 {
			return nil, failure(-32000, in.Class+" is a static key; replace its value instead")
		}
		info.Class, info.Provider = in.Class, in.Provider
		run = func(ctx context.Context, open func(string) error) (string, error) {
			return p.ID, store.Reauthorize(ctx, p.ID, open)
		}
	} else {
		var profile keychain.Profile
		if p.Provider == "custom" {
			if p.AuthURL == "" || p.TokenURL == "" {
				return nil, failure(-32602, "A custom provider needs its authorization and token endpoints")
			}
			profile = keychain.Custom(p.AuthURL, p.TokenURL, p.RevokeURL)
		} else {
			var ok bool
			if profile, ok = keychain.ProfileNamed(p.Provider); !ok {
				return nil, failure(-32602, fmt.Sprintf("No provider %q (%s, or custom)", p.Provider, strings.Join(keychain.ProfileNames(), ", ")))
			}
			if p.AuthURL != "" || p.TokenURL != "" || p.RevokeURL != "" {
				return nil, failure(-32602, "Endpoints are given only for a custom provider")
			}
		}
		class := p.Class
		if class == "" {
			class = profile.Name
		}
		if !keychain.ValidClass(class) {
			return nil, failure(-32602, keychain.BadClass)
		}
		grant := keychain.Grant{Profile: profile, ClientID: strings.TrimSpace(p.ClientID), ClientSecret: secret, Scopes: p.Scopes}
		info.Class, info.Provider = class, profile.Name
		run = func(ctx context.Context, open func(string) error) (string, error) {
			h, err := store.AddOAuth2(ctx, class, grant, open)
			if err != nil {
				return "", err
			}
			return h.ID, nil
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	consent := make(chan string, 1)
	early := make(chan error, 1)
	key := info.ID
	// Registered before the provider is asked for anything, so an
	// authorization that ends at once is not lost, and the consent page is
	// filled in when it exists.
	if s.authorizations == nil {
		s.authorizations = map[string]*authorization{}
	}
	registered := &authorization{info: info, cancel: cancel}
	s.authorizations[key] = registered
	go func() {
		id, err := run(ctx, func(url string) error {
			consent <- url
			return nil
		})
		cancel()
		early <- err // read only if it ends before the consent page exists
		s.mu.Lock()
		defer s.mu.Unlock()
		a, ok := s.authorizations[key]
		if !ok {
			return
		}
		a.ended, a.info.URL = time.Now(), ""
		if err != nil {
			a.info.State, a.info.Error = "failed", strings.TrimPrefix(err.Error(), "keychain: ")
			if errors.Is(err, context.Canceled) {
				a.info.Error = "nodwyn stopped before the provider answered"
			}
			return
		}
		a.info.State, a.info.Credential = "done", id
	}()
	// For OpenAI the consent page exists only after its discovery document
	// is fetched, so the wait is without the lifecycle lock; the page polls
	// credentials.list meanwhile.
	var url string
	var failed error
	s.slow("", func() {
		select {
		case url = <-consent:
		case failed = <-early:
		}
	})
	if failed != nil {
		delete(s.authorizations, key)
		return nil, failure(-32000, strings.TrimPrefix(failed.Error(), "keychain: "))
	}
	if registered.ended.IsZero() {
		registered.info.URL = url
	}
	return registered.info, nil
}
