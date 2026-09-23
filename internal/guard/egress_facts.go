package guard

import (
	"net/url"
	"strings"

	"github.com/fclaude/nibb/internal/scheme"
)

// egressFacts is what E3 needs to judge a call: whether it carries a credential,
// the credential's class, and the destination host, method and requested
// injection position if present.
type egressFacts struct {
	HasCredential bool
	Class         string // credential class; "" if the argument is not a handle
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
				f.Class = class
			}
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
// "method" role parameter, else "".
func methodArg(p *scheme.Prim, args []scheme.Value) string {
	for i, pa := range p.Meta.Params {
		if pa.Role == "method" && i < len(args) && args[i].Kind == scheme.KString {
			return args[i].S
		}
	}
	return ""
}

// positionArg returns the requested injection position if the primitive
// declares a "position" role parameter, else "".
func positionArg(p *scheme.Prim, args []scheme.Value) string {
	for i, pa := range p.Meta.Params {
		if pa.Role == "position" && i < len(args) && args[i].Kind == scheme.KString {
			return args[i].S
		}
	}
	return ""
}

// EgressScope describes the credential-bearing call being confirmed. Empty
// fields mean the primitive did not supply that fact; they are not wildcards.
// It is display metadata only and must not be used as permission to add a binding.
type EgressScope struct {
	Class    string
	Host     string
	Method   string
	Position string
}

func egressScopeOf(p *scheme.Prim, args []scheme.Value) *EgressScope {
	f := egressFactsOf(p, args)
	if !f.HasCredential {
		return nil
	}
	return &EgressScope{Class: f.Class, Host: f.Host, Method: f.Method, Position: f.Position}
}
