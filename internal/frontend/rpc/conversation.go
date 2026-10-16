package rpc

type conversationSearchParams struct {
	ID    string `json:"id"`
	Query string `json:"query"`
	Scope string `json:"scope,omitempty"`
	Limit int    `json:"limit,omitempty"`
}
type logContextParams struct {
	ID  string `json:"id"`
	Seq uint64 `json:"seq"`
}

func (s *server) searchConversation(req request, _ *peer) (any, *rpcError) {
	var p conversationSearchParams
	if err := params(req.Params, &p, "id", "query", "scope", "limit"); err != nil {
		return nil, err
	}
	if p.ID == "" {
		return nil, failure(-32602, "id is required")
	}
	if p.Scope == "" {
		p.Scope = "conversation"
	}
	if p.Limit == 0 {
		p.Limit = 20
	}
	if p.Limit < 1 || p.Limit > 20 || (p.Scope != "conversation" && p.Scope != "earlier") {
		return nil, failure(-32602, "scope must be conversation or earlier; limit must be 1..20")
	}
	store := s.store()
	// A first search may build derived word indexes. It must not hold up
	// lifecycle operations for other clients while reading these logs.
	s.mu.Unlock()
	result, err := store.SearchConversations(p.ID, p.Scope, p.Query, p.Limit)
	s.mu.Lock()
	if err != nil {
		return nil, failure(-32000, err.Error())
	}
	return result, nil
}
func (s *server) readContext(req request, _ *peer) (any, *rpcError) {
	var p logContextParams
	if err := params(req.Params, &p, "id", "seq"); err != nil {
		return nil, err
	}
	if p.ID == "" || p.Seq == 0 {
		return nil, failure(-32602, "id and seq are required")
	}
	result, err := s.store().ReadContext(p.ID, p.Seq)
	if err != nil {
		return nil, failure(-32000, err.Error())
	}
	return result, nil
}
