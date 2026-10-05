package rpc

// These structs are the implemented wire contract. The schema generator reads
// their JSON names; optional request fields use omitempty to express defaults.
type helloParams struct {
	Protocol int `json:"protocol"`
	// Via names the screen a relaying client answers for, such as a paired
	// phone behind the browser bridge (ADR 30). It is appended to the kernel
	// identity as a claim and never replaces it.
	Via string `json:"via,omitempty"`
}
type helloResult struct {
	Protocol int      `json:"protocol"`
	Version  string   `json:"version"`
	Methods  []string `json:"methods"`
	Peer     string   `json:"peer"`
}
type openParams struct {
	Repo string `json:"repo"`
	// Name, Server and Model choose the model the conversation starts on,
	// as session.model.set does (ADR 40). Without them it is config.toml's
	// [control] model.
	Name   string `json:"name,omitempty"`
	Server string `json:"server,omitempty"`
	Model  string `json:"model,omitempty"`
	// Effort is the reasoning effort the conversation starts with: one the
	// model takes (ADR 41). Left out, it is the model's own default.
	Effort string `json:"effort,omitempty"`
}

// modelSetParams choose a conversation's model: a [models.NAME] entry by
// name (or "control" for config.toml's [control] model), or a model on one
// of the configured [servers.NAME].
type modelSetParams struct {
	ID     string `json:"id"`
	Name   string `json:"name,omitempty"`
	Server string `json:"server,omitempty"`
	Model  string `json:"model,omitempty"`
	// Effort is the reasoning effort its requests ask for, one the model
	// takes; left out, the model's own default (ADR 41). The effort alone is
	// changed by naming the model the conversation is on again.
	Effort string `json:"effort,omitempty"`
}
type idParams struct {
	ID string `json:"id"`
}

// renameParams name a session; an empty title takes the name back (ADR 39).
type renameParams struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}
type archiveParams struct {
	ID       string `json:"id"`
	Archived bool   `json:"archived"`
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

type inspectionStarted struct {
	OperationID string `json:"operation_id"`
}
