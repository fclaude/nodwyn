package rpc

import "strings"

// Shared parameter types also generate the wire schema. Credentials are only
// decoded here, never included in a response, model message or diagnostic.
type answerParams struct {
	ID       string `json:"id"`
	PromptID string `json:"prompt_id"`
	Reply    string `json:"reply"`
}
type approvalParams struct {
	ID       string `json:"id"`
	PromptID string `json:"prompt_id"`
	Scope    string `json:"scope"`
}
type bindingParams struct {
	ID       string `json:"id"`
	PromptID string `json:"prompt_id"`
}
type approveAllParams struct {
	ID string `json:"id"`
	On bool   `json:"on"`
}
type secretParams struct {
	ID       string `json:"id"`
	PromptID string `json:"prompt_id"`
	Secret   string `json:"secret"`
}

func (s *server) answer(req request, client *peer) (any, *rpcError) {
	if client.actor == "" {
		return nil, failure(-32003, "Peer identity is unavailable")
	}
	var id, prompt string
	var apply func() error
	switch req.Method {
	case "session.answer":
		var p answerParams
		if err := params(req.Params, &p, "id", "prompt_id", "reply"); err != nil {
			return nil, err
		}
		id, prompt = p.ID, p.PromptID
		apply = func() error { return s.sessions[id].session.AnswerFrom(prompt, p.Reply, client.actor) }
	case "session.approval.create":
		var p approvalParams
		if err := params(req.Params, &p, "id", "prompt_id", "scope"); err != nil {
			return nil, err
		}
		id, prompt = p.ID, p.PromptID
		apply = func() error { return s.sessions[id].session.CreateApproval(prompt, p.Scope, client.actor) }
	case "session.binding.create":
		var p bindingParams
		if err := params(req.Params, &p, "id", "prompt_id"); err != nil {
			return nil, err
		}
		id, prompt = p.ID, p.PromptID
		apply = func() error { return s.sessions[id].session.CreateBinding(prompt, client.actor) }
	case "session.secret":
		var p secretParams
		if err := params(req.Params, &p, "id", "prompt_id", "secret"); err != nil {
			return nil, err
		}
		id, prompt = p.ID, p.PromptID
		apply = func() error {
			secret := []byte(p.Secret)
			defer clear(secret)
			return s.sessions[id].session.SecretReplyFrom(prompt, secret, client.actor)
		}
	}
	if id == "" || prompt == "" {
		return nil, failure(-32602, "id and prompt_id are required")
	}
	if _, ok := s.sessions[id]; !ok {
		return nil, failure(-32002, "Session is not open in this daemon")
	}
	if err := apply(); err != nil {
		return nil, failure(-32000, err.Error())
	}
	return struct{}{}, nil
}

// approveAll turns "don't ask" on or off for an open session: its ordinary
// writes and commands are approved without a prompt. Turning it on widens
// what the session does unasked, so like a run that answers itself it is
// for the computer nodwyn runs on (ADR 30): a paired device's relayed actor
// may only turn it off.
func (s *server) approveAll(req request, client *peer) (any, *rpcError) {
	if client.actor == "" {
		return nil, failure(-32003, "Peer identity is unavailable")
	}
	var p approveAllParams
	if err := params(req.Params, &p, "id", "on"); err != nil {
		return nil, err
	}
	open, ok := s.sessions[p.ID]
	if !ok {
		return nil, failure(-32002, "Session is not open in this daemon")
	}
	if p.On && strings.Contains(client.actor, " via ") {
		return nil, failure(-32602, "Approving without asking is turned on only on the computer nodwyn runs on, not from a paired device")
	}
	if err := open.session.SetApproveAll(p.On, client.actor); err != nil {
		return nil, failure(-32000, err.Error())
	}
	return struct{}{}, nil
}
