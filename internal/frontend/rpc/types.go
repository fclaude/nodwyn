package rpc

// These structs are the implemented wire contract. The schema generator reads
// their JSON names; optional request fields use omitempty to express defaults.
type helloParams struct {
	Protocol int `json:"protocol"`
	// Via names the screen a relaying client answers for, such as a paired
	// phone behind the browser bridge (ADR 30). It is appended to the kernel
	// identity as a claim and never replaces it.
	Via string `json:"via,omitempty"`
	// Passkey names the passkey a relayed device's person proved themself
	// with (ADR 30, amended 2026-10-17). With it, the device is not limited
	// as a paired device is; it is recorded after via, as passkey:NAME.
	Passkey string `json:"passkey,omitempty"`
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
	// Judge is the [servers.NAME] the task's judge is on, "" for none
	// (ADR 47). Left out, it is config.toml's [judge] server.
	Judge *string `json:"judge,omitempty"`
	// Delegates are the [models.NAME] the task's model may hand work to with
	// its delegate tool (ADR 27, amended 2026-10-09). Left out, none: the task
	// talks to its own model alone. Fixed for the task, since the tool list is.
	Delegates []string `json:"delegates,omitempty"`
	// Limits false opens a task whose turns have no turn limits (ADR 49).
	// Left out, or true, its turns stop at Settings' limits.
	Limits *bool `json:"limits,omitempty"`
}

// limitsSetParams turn a task's turn limits on or off (ADR 49).
type limitsSetParams struct {
	ID string `json:"id"`
	On bool   `json:"on"`
}

// limitsState is whether a task's turns stop at the turn limits.
type limitsState struct {
	On bool `json:"on"`
}

// judgeSetParams choose a task's judge: a [servers.NAME] whose provider
// answers typed questions, or "" for none (ADR 47).
type judgeSetParams struct {
	ID    string `json:"id"`
	Judge string `json:"judge"`
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
	LatestRun bool   `json:"latest_run,omitempty"`
	ID        string `json:"id"`
	After     uint64 `json:"after,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}
type readParams struct {
	LatestRun bool   `json:"latest_run,omitempty"`
	ID        string `json:"id"`
	After     uint64 `json:"after,omitempty"`
	Through   uint64 `json:"through,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

type evalParams struct {
	ID     string `json:"id"`
	Source string `json:"source"`
}

type inspectionStarted struct {
	OperationID string `json:"operation_id"`
}

// runsParams fixes a prefix while paging backwards through run previews.
type runsParams struct {
	ID      string `json:"id"`
	Before  uint64 `json:"before,omitempty"`
	Through uint64 `json:"through,omitempty"`
	Limit   int    `json:"limit,omitempty"`
}
