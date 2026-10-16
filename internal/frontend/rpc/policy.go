package rpc

type policyReadParams struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type policyWriteParams struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Revision string `json:"revision"`
	Content  string `json:"content"`
}

func (s *server) policy(req request, client *peer) (any, *rpcError) {
	var p policyWriteParams
	if req.Method == "session.policy.read" {
		var r policyReadParams
		if err := params(req.Params, &r, "id", "name"); err != nil {
			return nil, err
		}
		p.ID, p.Name = r.ID, r.Name
	} else if err := params(req.Params, &p, "id", "name", "revision", "content"); err != nil {
		return nil, err
	}
	owned, rerr := s.idle(p.ID, "editing policy")
	if rerr != nil {
		return nil, rerr
	}
	var operation string
	var err error
	if req.Method == "session.policy.read" {
		operation, err = owned.session.ReadPolicy(p.Name, client.actor)
	} else {
		operation, err = owned.session.WritePolicy(p.Name, p.Revision, p.Content, client.actor)
	}
	if err != nil {
		return nil, failure(-32000, err.Error())
	}
	return inspectionStarted{operation}, nil
}
