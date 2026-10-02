package guard

import (
	"net/url"
	"strings"

	"github.com/fclaude/nodwyn/internal/binding"
	"github.com/fclaude/nodwyn/internal/scheme"
)

// egressFacts is what E3 needs to judge a call: whether it carries a credential,
// the credential's class, and the destination host, method and requested
// injection position if present.
type egressFacts struct {
	HasCredential bool
	Class         string // credential class; "" if the argument is not a handle
	HandleID      string // the handle's id; "" for a class the primitive injects itself
	Host          string // destination hostname (lower-cased)
	Method        string // HTTP method, if the prim declares one
	Position      string // requested injection position, e.g. "header:Authorization"
}

// egressFactsOf extracts the egress facts of a primitive application.
func egressFactsOf(p *scheme.Prim, args []scheme.Value) egressFacts {
	var f egressFacts
	if p.Meta.Egress {
		if i := credIndex(p, args); i >= 0 {
			// HasCredential is true only when the credential slot is actually
			// filled with a handle. A non-handle in the slot is not a valid
			// credential-bearing call: the guard's other checkers still apply,
			// but E3 has no binding to check.
			if class, ok := credClass(args[i]); ok {
				f.HasCredential = true
				f.Class, f.HandleID = class, args[i].H.ID
			}
		}
		if !f.HasCredential && p.Meta.EgressClass != "" && credIndex(p, args) < 0 {
			// A host integration that injects its own credential: the call
			// carries that class whatever the arguments say.
			f.HasCredential = true
			f.Class = p.Meta.EgressClass
		}
		f.Host = destHost(p, args)
		if f.Host == "" && p.Meta.EgressHost != "" {
			f.Host = strings.ToLower(p.Meta.EgressHost)
		}
		f.Method = methodArg(p, args)
		f.Position = positionArg(p, args)
	}
	return f
}

// credIndex returns the argument index of the primitive's credential, or -1.
func credIndex(p *scheme.Prim, args []scheme.Value) int {
	for i, pa := range p.Meta.Params {
		if pa.Role == "credential" && i < len(args) {
			return i
		}
	}
	return -1
}

// credClass returns the credential's class (from the handle), or "" if the
// argument is not a handle.
func credClass(v scheme.Value) (string, bool) {
	if v.Kind != scheme.KHandle {
		return "", false
	}
	return v.H.Class, true
}

// destHost returns the destination hostname: the URL argument's host if the
// primitive declares a "dest" role parameter, else "".
func destHost(p *scheme.Prim, args []scheme.Value) string {
	for i, pa := range p.Meta.Params {
		if pa.Role == "dest" && i < len(args) && args[i].Kind == scheme.KString {
			if u, err := url.Parse(args[i].S); err == nil {
				return strings.ToLower(u.Hostname())
			}
			return strings.ToLower(args[i].S)
		}
	}
	return ""
}

// methodArg returns the method argument if the primitive declares a
// "method" role parameter, else its fixed host metadata.
func methodArg(p *scheme.Prim, args []scheme.Value) string {
	for i, pa := range p.Meta.Params {
		if pa.Role == "method" {
			if i < len(args) && args[i].Kind == scheme.KString {
				return args[i].S
			}
			return ""
		}
	}
	return p.Meta.EgressMethod
}

// positionArg uses the declared argument, or fixed host metadata if absent.
func positionArg(p *scheme.Prim, args []scheme.Value) string {
	for i, pa := range p.Meta.Params {
		if pa.Role == "position" {
			if i < len(args) && args[i].Kind == scheme.KString {
				return args[i].S
			}
			return ""
		}
	}
	return p.Meta.EgressPosition
}

// boundFor reports whether a key of class may go where the call sends it:
// to the destination host, by the call's method; or, for a contained MCP
// server's tool, on to every host its container can reach, since the
// arguments may go to any of them. A container that reaches no host is
// where no binding points, so nothing is bound for it.
func boundFor(t binding.Authority, p *scheme.Prim, f egressFacts, class string) (bool, string) {
	if !p.Meta.EgressContained {
		return t.Lookup(class, f.Host, f.Method, "")
	}
	if len(p.Meta.EgressOnward) == 0 {
		return false, destOf(f) + " has no network, so no binding names where it sends"
	}
	for _, host := range p.Meta.EgressOnward {
		if bound, reason := t.Lookup(class, strings.ToLower(host), "", ""); !bound {
			return false, destOf(f) + " can reach " + host + ": " + reason
		}
	}
	return true, ""
}

// EgressScope describes the credential-bearing call being confirmed. Empty
// fields mean the primitive did not supply that fact; they are not wildcards.
// It is display metadata only and must not be used as permission to add a binding.
// Scopes are an OAuth2 credential's granted scopes: "Google Drive, files
// this app created" and "Google Drive, everything" differ only there
// (ADR 24 §4).
type EgressScope struct {
	Class    string
	Host     string
	Method   string
	Position string
	Scopes   []string `json:",omitempty"`
}

// CredentialScopes returns the scopes a credential was granted: the one
// with handle id, or for "" the one a primitive of class injects itself.
// A static key has none.
type CredentialScopes func(class, id string) []string

func (f egressFacts) scopes(of CredentialScopes) []string {
	if of == nil || !f.HasCredential {
		return nil
	}
	return of(f.Class, f.HandleID)
}

func egressScopeOf(p *scheme.Prim, args []scheme.Value, scopes CredentialScopes) *EgressScope {
	f := egressFactsOf(p, args)
	if !f.HasCredential {
		return nil
	}
	return &EgressScope{Class: f.Class, Host: f.Host, Method: f.Method, Position: f.Position, Scopes: f.scopes(scopes)}
}
