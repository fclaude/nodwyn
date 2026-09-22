package rpc

// These structs are the implemented wire contract. The schema generator reads
// their JSON names; optional request fields use omitempty to express defaults.
type helloParams struct {
	Protocol int `json:"protocol"`
}
type helloResult struct {
	Protocol int      `json:"protocol"`
	Version  string   `json:"version"`
	Methods  []string `json:"methods"`
	Peer     string   `json:"peer"`
}
type openParams struct {
	Repo string `json:"repo"`
}
type idParams struct {
	ID string `json:"id"`
}
type sendParams struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}
type subscribeParams struct {
	ID    string `json:"id"`
	After uint64 `json:"after,omitempty"`
	Limit int    `json:"limit,omitempty"`
}
type readParams struct {
	ID      string `json:"id"`
	After   uint64 `json:"after,omitempty"`
	Through uint64 `json:"through,omitempty"`
	Limit   int    `json:"limit,omitempty"`
}

type evalParams struct {
	ID     string `json:"id"`
	Source string `json:"source"`
}
