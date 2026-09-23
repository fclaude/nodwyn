package rpc

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
