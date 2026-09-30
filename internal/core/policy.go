package core

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"

	"github.com/fclaude/nodwyn/internal/event"
	"github.com/fclaude/nodwyn/internal/prim"
	"github.com/fclaude/nodwyn/internal/scheme"
)

// ReadPolicy returns a fixed configured policy file (bindings or approvals)
// for a front-end's inspector, through the same guarded path as policy-read.
func (s *Session) ReadPolicy(name, actor string) (string, error) {
	return s.policyOperation(name, "", "", actor, false)
}

// WritePolicy requests a confirmed replacement of a fixed configured policy.
// The revision is compared under the persistence lock after confirmation.
func (s *Session) WritePolicy(name, revision, content, actor string) (string, error) {
	if revision == "" {
		return "", fmt.Errorf("policy revision is required")
	}
	if err := prim.ValidatePolicy(name, content); err != nil {
		return "", err
	}
	return s.policyOperation(name, revision, content, actor, true)
}

func (s *Session) policyOperation(name, revision, content, actor string, write bool) (string, error) {
	if actor == "" || (name != "bindings" && name != "approvals") {
		return "", fmt.Errorf("policy: actor and a supported policy name are required")
	}
	id := "i" + rand.Text()
	err := s.startTurn(func(ctx context.Context) error {
		op := "policy-read"
		args := []scheme.Value{scheme.Str(name)}
		if write {
			op = "policy-write"
			args = append(args, scheme.Str(revision), scheme.Str(content))
		}
		result := event.Policy{ID: id, Name: name}
		// Lookup the registered host primitive itself. An environment definition with
		// the same name must never redirect a frontend operation to Scheme code.
		p, ok := s.interp.Lookup(op)
		var value scheme.Value
		var err error
		if !ok {
			err = fmt.Errorf("policy operations are unavailable")
		} else {
			source := scheme.Write(scheme.List(append([]scheme.Value{scheme.Sym(op)}, args...)...))
			value, err = s.recordEvaluation(source, actor, func() (scheme.Value, error) {
				return s.interp.Apply(scheme.WithOrigin(ctx, scheme.OriginUser), scheme.Value{Kind: scheme.KPrim, Prim: p}, args)
			})
		}
		if err == nil && !write {
			raw, encodeErr := scheme.ToJSON(value)
			var doc prim.PolicyDocument
			if encodeErr != nil {
				err = encodeErr
			} else {
				err = json.Unmarshal(raw, &doc)
			}
			if err == nil {
				result.Content = doc.Content
				result.Revision = doc.Revision
				result.Exists = doc.Exists
			}
		}
		if err != nil {
			result.Err = err.Error()
		} else {
			result.Saved = write
		}
		s.Emit(event.Event{Kind: event.PolicyResult, Payload: result})
		return err
	})
	if err != nil {
		return "", err
	}
	return id, nil
}
