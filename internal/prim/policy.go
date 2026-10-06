package prim

import (
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/fclaude/nodwyn/internal/binding"
	"github.com/fclaude/nodwyn/internal/config"
	"github.com/fclaude/nodwyn/internal/guard"
	"github.com/fclaude/nodwyn/internal/policyfile"
	"github.com/fclaude/nodwyn/internal/redact"
	"github.com/fclaude/nodwyn/internal/scheme"
)

// MaxPolicyBytes bounds a policy file read or written through
// policy-read and policy-write.
const MaxPolicyBytes = 64 << 10

// PolicyDocument is configuration text, not a restored grant. Binding changes
// apply at the next egress check; approval files need fresh project trust before new sessions load them.
type PolicyDocument struct {
	Content  string `json:"content"`
	Revision string `json:"revision"`
	Exists   bool   `json:"exists"`
}

func policyPath(o Options, name string) (string, error) {
	switch name {
	case "bindings":
		if o.BindingsPath != "" {
			return o.BindingsPath, nil
		}
	case "approvals":
		path := (guard.RepoPin{Root: o.Root}).Resolve(filepath.Join(".nodwyn", "allow.toml"))
		if path != "" {
			return path, nil
		}
	}
	return "", fmt.Errorf("policy: unavailable policy %q", name)
}

// ValidatePolicy rejects unknown names, malformed policy and secret-bearing text
// before an editor sends it to evaluation history or asks for a diff approval.
func ValidatePolicy(name, content string) error {
	if len(content) > MaxPolicyBytes {
		return fmt.Errorf("policy: content exceeds %d bytes", MaxPolicyBytes)
	}
	if len(redact.Redact(content, nil).Events) > 0 {
		return fmt.Errorf("policy: text contains a recognized secret; remove it locally before editing here")
	}
	switch name {
	case "bindings":
		_, err := binding.Parse([]byte(content))
		return err
	case "approvals":
		_, err := config.ParseRepoAllow([]byte(content))
		return err
	default:
		return fmt.Errorf("policy: unknown policy %q", name)
	}
}

func readPolicy(o Options, name string) (PolicyDocument, error) {
	path, err := policyPath(o, name)
	if err != nil {
		return PolicyDocument{}, err
	}
	raw, err := policyfile.Read(path, MaxPolicyBytes)
	if err != nil {
		return PolicyDocument{}, err
	}
	if len(redact.Redact(string(raw), nil).Events) > 0 {
		return PolicyDocument{}, fmt.Errorf("policy: file contains a recognized secret; edit it locally")
	}
	return PolicyDocument{Content: string(raw), Revision: policyfile.Revision(raw), Exists: raw != nil}, nil
}

func policyRead(o Options) *scheme.Prim {
	return &scheme.Prim{
		Name: "policy-read",
		Meta: scheme.Meta{Doc: "Read one configured policy file (bindings or approvals); user only.", Params: []scheme.Param{{Name: "policy", Type: "string"}}, Host: true, Hidden: true, Audited: true, TaintSource: scheme.TagFile},
		Handler: func(c *scheme.Call) (scheme.Value, error) {
			if c.Origin != scheme.OriginUser {
				return scheme.Void, fmt.Errorf("policy reads require user origin")
			}
			name, err := c.String(0)
			if err != nil {
				return scheme.Void, err
			}
			doc, err := readPolicy(o, name)
			if err != nil {
				return scheme.Void, err
			}
			raw, err := json.Marshal(doc)
			if err != nil {
				return scheme.Void, err
			}
			return scheme.FromJSON(raw)
		},
	}
}

func policyWrite(o Options) *scheme.Prim {
	return &scheme.Prim{
		Name: "policy-write",
		// Policy selectors are not paths. The host maps these two names to its own
		// configured files; no user-supplied filesystem path or standing grant applies.
		Meta: scheme.Meta{Doc: "Replace one configured policy file after revision comparison and confirmation; user only.",
			Params: []scheme.Param{{Name: "policy", Type: "string"}, {Name: "revision", Type: "string"}, {Name: "content", Type: "string", Role: "payload"}},
			Host:   true, Hidden: true, Audited: true, Irreversible: true,
			Preview: func(args []scheme.Value) string {
				if len(args) != 3 {
					return ""
				}
				doc, err := readPolicy(o, args[0].S)
				if err != nil {
					return "Cannot load policy preview: " + err.Error()
				}
				scope := "Shared credential bindings; applies before the next egress operation."
				if args[0].S == "approvals" {
					scope = "Repository standing approvals; review the saved revision with nodwyn trust before newly opened or resumed sessions load it. Existing sessions retain their current approvals."
				}
				return scope + "\n" + Unified(args[0].S, doc.Content, args[2].S, 3)
			},
		},
		Handler: func(c *scheme.Call) (scheme.Value, error) {
			if c.Origin != scheme.OriginUser {
				return scheme.Void, fmt.Errorf("policy writes require user origin")
			}
			name, err := c.String(0)
			if err != nil {
				return scheme.Void, err
			}
			revision, err := c.String(1)
			if err != nil {
				return scheme.Void, err
			}
			content, err := c.String(2)
			if err != nil {
				return scheme.Void, err
			}
			if err := ValidatePolicy(name, content); err != nil {
				return scheme.Void, err
			}
			path, err := policyPath(o, name)
			if err != nil {
				return scheme.Void, err
			}
			err = policyfile.Update(c.Ctx, path, func(raw []byte) ([]byte, error) {
				if policyfile.Revision(raw) != revision {
					return nil, fmt.Errorf("policy changed; read it again and review a new diff")
				}
				return []byte(content), nil
			})
			return scheme.Void, err
		},
	}
}
