package log

import (
	"encoding/json"
	"regexp"
	"strings"
	"time"

	"github.com/fclaude/nodwyn/internal/binding"
	"github.com/fclaude/nodwyn/internal/budget"
)

// Typed payloads for the event kinds. They are the schema of the log; the
// window, the replay command and every front-end decode these.

// SessionStart is the first event of every session.
type SessionStart struct {
	Context  *WindowContext `json:"context,omitempty"`
	Version  string         `json:"version"`
	Repo     string         `json:"repo"`
	Provider string         `json:"provider"`
	Model    string         `json:"model"`
	URL      string         `json:"url"`
	Tools    int            `json:"tools"`
	// Worktree is the git worktree the session works in (ADR 34); Repo is
	// then its path. nil for a session in the folder itself.
	Worktree *Worktree `json:"worktree,omitempty"`
}

// Worktree is a session's own linked git worktree (ADR 34): made for it in
// Repository's .nodwyn/worktrees/, on Branch, from Base (the branch the
// repository had checked out, "" when its HEAD was detached) at BaseCommit.
type Worktree struct {
	Repository string `json:"repository"`
	Path       string `json:"path"`
	Branch     string `json:"branch"`
	Base       string `json:"base"`
	BaseCommit string `json:"base_commit"`
}

// WorktreeChange records a session's worktree made (worktree.create), made
// again on resume (worktree.restore) or removed on close (worktree.remove).
// Uncommitted counts the repository's uncommitted and untracked files when
// it was made, which the worktree does not have.
type WorktreeChange struct {
	Worktree
	Uncommitted int `json:"uncommitted,omitempty"`
}

// SessionEnd is written on clean shutdown.
type SessionEnd struct {
	Reason string `json:"reason"`
}

// UserMsg is text the user sent.
type UserMsg struct {
	Text string `json:"text"`
}

// HarnessMsg is text the loop's strategy added to the conversation with
// add-message. The model reads it as a user message; it never changes the
// current task.
type HarnessMsg struct {
	Text string `json:"text"`
}

// ContextNarrow starts a narrowed context: until a widen or the next user
// message, the model sees System as its system prompt and only what comes
// after this event.
type ContextNarrow struct {
	System string `json:"system"`
	// Agent names who works in this context, such as a sub-agent's tool
	// name; Answers lists the model's calls waiting for it to fold. Neither
	// changes the window: they let a log be read agent by agent.
	Agent   string   `json:"agent,omitempty"`
	Answers []string `json:"answers,omitempty"`
	// Lasting marks a narrowing that outlasts its turn, such as a
	// compaction's: if it is still open at the next user message, the
	// conversation starts here from then on, under the session's system
	// prompt (window recipe v11).
	Lasting bool `json:"lasting,omitempty"`
}

// ContextWiden ends a narrowed context.
type ContextWiden struct{}

// ContextFold ends the innermost narrowed context and folds it away: later
// windows skip everything since its narrow, and see the context around it
// again.
type ContextFold struct{}

// ContextCompact records one compaction (compact.scm, record-compaction):
// what set it off (auto: the context passed Threshold; command: /compact;
// requested: a strategy's compact-next-round), whether it compacted, and the
// context's size before and after, in the window's estimated tokens (the
// unit of compact-threshold, not the provider's count). The handover note is
// the llm.resp at NoteSeq: NoteTokens long, Truncated when it stopped at the
// output limit. A compaction that gave up (no note came back) has
// Compacted false and no After. Windows ignore it.
type ContextCompact struct {
	Trigger    string `json:"trigger"`
	Compacted  bool   `json:"compacted"`
	Before     int    `json:"before_tokens"`
	After      int    `json:"after_tokens,omitempty"`
	Threshold  int    `json:"threshold,omitempty"`
	Focus      string `json:"focus,omitempty"`
	NoteSeq    uint64 `json:"note_seq,omitempty"`
	NoteTokens int    `json:"note_tokens,omitempty"`
	Truncated  bool   `json:"truncated,omitempty"`
}

// ResultFold replaces what later windows show for one tool result (Seq)
// with Text: an outline, or a line saying what it was, once the details
// stopped mattering (fold-result). The result itself stays in the log.
type ResultFold struct {
	Seq  uint64 `json:"seq"`
	Text string `json:"text"`
}

// LLMReq describes a request to the control-plane model. The messages are
// not repeated: they are rebuilt from the log by the window.
type LLMReq struct {
	RequestHash string   `json:"request_hash,omitempty"`
	Model       string   `json:"model"`
	Messages    int      `json:"messages"`
	Tools       int      `json:"tools"`
	EstTokens   int      `json:"est_tokens"`
	Elided      []uint64 `json:"elided,omitempty"`
	// Endpoint is the registry name when a strategy asked a model other
	// than the control model ([models.NAME]); NumCtx and KeepAlive are then
	// that request's, since they differ from the session's recipe.
	Endpoint  string `json:"endpoint,omitempty"`
	NumCtx    int    `json:"num_ctx,omitempty"`
	KeepAlive string `json:"keep_alive,omitempty"`
	// NoTools is set when the request offered the model no tools, as the
	// planner's does ((model-turn name '((tools . #f)))).
	NoTools bool `json:"no_tools,omitempty"`
	// OfferedTools names the tools offered when a request offered only
	// some of them ((model-turn name '((tools "fs-read" ...)))), as a
	// sub-agent's requests do; empty when it offered all or none.
	OfferedTools []string `json:"offered_tools,omitempty"`
	// MaxOutput is the request's reply cap when its model sets its own
	// (max_output), instead of the recipe's window.output_reserve.
	MaxOutput int `json:"max_output,omitempty"`
	// Temperature and Seed are the request's sampling settings, when its
	// model sets them.
	Temperature *float64 `json:"temperature,omitempty"`
	Seed        *int64   `json:"seed,omitempty"`
	// Effort is the reasoning effort the request asked for, when it asked for
	// one (ADR 41); empty left the model to its provider's default. It is in
	// the request's hash, so replay needs it.
	Effort string `json:"effort,omitempty"`
	// HighWater is the size past which this request's window was trimmed,
	// when it was not the recipe's: a request that may use the room above
	// the high water ((model-turn name '((headroom . #t))), as the handover
	// note's does) is trimmed only past the ceiling (window recipe v10).
	HighWater int `json:"high_water,omitempty"`
	// Withheld are the attached images in the window that this request did
	// not carry, and why (window recipe v12).
	Withheld []ImageGap `json:"withheld_images,omitempty"`
}

// ToolCall is one tool application the model asked for.
type ToolCall struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

// Usage is what the provider reported.
type Usage struct {
	PromptTokens int   `json:"prompt_tokens"`
	OutputTokens int   `json:"output_tokens"`
	PromptEvalMS int64 `json:"prompt_eval_ms"`
	EvalMS       int64 `json:"eval_ms"`
	TotalMS      int64 `json:"total_ms"`
	LoadMS       int64 `json:"load_ms"`
	// WallMS is how long the request took as this machine saw it, from send
	// to the reply's complete arrival, the successful attempt only (a
	// retry's earlier failures are not the model's time). Host-measured, so
	// a provider that reports no timings of its own has it too. Older logs
	// lack it.
	WallMS int64 `json:"wall_ms,omitempty"`
	// CachedTokens are the prompt tokens the provider read from its cache,
	// when it reports them: absent (an older log, Ollama) is unknown, not 0.
	CachedTokens     *int `json:"cached_tokens,omitempty"`
	CacheWriteTokens int  `json:"cache_write_tokens,omitempty"`
	ReasoningTokens  int  `json:"reasoning_tokens,omitempty"`
	// Cost is the request's cost in USD: reported by the provider, or, with
	// CostEstimated, estimated from the model's configured prices. Absent is
	// unknown, never free.
	Cost          *float64 `json:"cost,omitempty"`
	CostEstimated bool     `json:"cost_estimated,omitempty"`
	// Unreported: the server gave no token counts; the zeros are not counts.
	Unreported bool `json:"unreported,omitempty"`
}

// ThinkingBlock is llm.ThinkingBlock as the log keeps it.
type ThinkingBlock struct {
	Type      string `json:"type"`
	Thinking  string `json:"thinking,omitempty"`
	Signature string `json:"signature,omitempty"`
	Data      string `json:"data,omitempty"`
	Before    int    `json:"before,omitempty"`
	Origin    string `json:"origin,omitempty"`
}

// LLMResp is the model's full output for one request.
type LLMResp struct {
	Content  string `json:"content"`
	Thinking string `json:"thinking,omitempty"`
	// Blocks are the reply's reasoning as its provider signed it, kept to
	// be given back with the reply (llm.ThinkingBlock, window recipe v18).
	Blocks     []ThinkingBlock `json:"blocks,omitempty"`
	ToolCalls  []ToolCall      `json:"tool_calls,omitempty"`
	DoneReason string          `json:"done_reason"`
	Usage      Usage           `json:"usage"`
}

// ToolCallEvent is written before a tool application is evaluated.
type ToolCallEvent struct {
	ToolCall
	Origin string `json:"origin"`
}

// ToolResult is written after it. Exactly one of Value or Error is set;
// Value is what the model is shown.
type ToolResult struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Value  string `json:"value,omitempty"`
	Error  string `json:"error,omitempty"`
	Millis int64  `json:"ms"`
}

// GuardDecision records the guard's answer at apply->Prim.
type GuardDecision struct {
	Actor    string `json:"actor,omitempty"`
	PromptID string `json:"prompt_id,omitempty"`
	Scope    string `json:"scope,omitempty"`
	Prim     string `json:"prim"`
	Origin   string `json:"origin"`
	Decision string `json:"decision"` // allow | deny | confirm
	Checker  string `json:"checker,omitempty"`
	Reason   string `json:"reason,omitempty"`
	Answer   string `json:"answer,omitempty"` // for confirm: yes | no | turn
	Args     string `json:"args,omitempty"`   // short written form of the arguments
	Detail   string `json:"detail,omitempty"` // the diff, when the user did not see it (standing approvals)
	// Path is the file a write names, as the call wrote it, so a reader can
	// list a session's changes from its decisions. Older logs lack it.
	Path string `json:"path,omitempty"`
}

// EgressCheck records a binding lookup for a secret-bearing egress (E3).
type EgressCheck struct {
	Prim   string   `json:"prim"`
	Class  string   `json:"class"`
	Host   string   `json:"host,omitempty"`
	Method string   `json:"method,omitempty"`
	Scopes []string `json:"scopes,omitempty"` // an OAuth2 credential's granted scopes (ADR 24)
	Bound  bool     `json:"bound"`
	Reason string   `json:"reason,omitempty"` // set when not bound
}

// GatePlan is the P5 gate outcome for one user message: the noul verdict,
// whether a plan was required, the Seam A score, and the revision count.
// A run's later turn, and the turn after one that did not finish, carry
// the plan instead of drafting one ("carried"), with Marks, the steps the
// earlier turns marked, the latest mark per step in step order. The model
// may write its own plan ("model", with plan-write).
type GatePlan struct {
	Origin    string     `json:"origin"`
	Noul      string     `json:"noul"` // "plan" | "proceed" | "degraded" | "required" (a run's turn, no judge) | "carried" (a run's later turn, or the one after a turn that did not finish) | "model" (plan-write)
	Plan      string     `json:"plan,omitempty"`
	Score     float64    `json:"score,omitempty"`
	Revisions int        `json:"revisions,omitempty"`
	Reason    string     `json:"reason,omitempty"`
	Marks     []PlanStep `json:"marks,omitempty"`
}

// planMarker is what starts a step's line: "1." "2)" "-" "*" "•".
var planMarker = regexp.MustCompile(`^(\d+[.)]|[-*•])\s+`)

// Steps reads the plan's numbered or bulleted lines as its steps, in order;
// a plan in prose is one step. Step n of a plan.step is Steps()[n-1]. The
// console reads a plan the same way (transcript.js planSteps).
func (g GatePlan) Steps() []string {
	var lines, steps []string
	for _, l := range strings.Split(g.Plan, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	for _, l := range lines {
		if m := planMarker.FindString(l); m != "" {
			steps = append(steps, l[len(m):])
		}
	}
	if len(steps) == 0 && len(lines) > 0 {
		return []string{strings.Join(lines, " ")}
	}
	return steps
}

// PlanStep is the model marking step Step (of Of) of the turn's recorded
// plan done or skipped, with Note saying what shows it. It is the model's
// claim, not a proof: the evidence is the transcript around it. A later mark
// of the same step replaces an earlier one, in a later turn of a run too.
type PlanStep struct {
	Step  int    `json:"step"`
	Of    int    `json:"of"`
	State string `json:"state"` // "done" | "skipped"
	Note  string `json:"note,omitempty"`
}

// JevReq is one Jev egress (ADR 19/20): what left, how much, and the score.
// The payload hash is a short digest; the raw payload is never logged, and a
// redacted payload is never logged either (only the events it triggered).
type JevReq struct {
	Tool        string   `json:"tool"`                 // judge-yes-no, judge-score or judge-choice (jev-noul, jev-score, jev-choice, judge-noul before ADR 46)
	Origin      string   `json:"origin"`               // user, model or harness
	Level       string   `json:"level"`                // none | plan | plan+summaries | full
	Bytes       int      `json:"bytes"`                // bytes of the redacted payload sent
	PayloadHash string   `json:"payload_hash"`         // hex sha256[:16] of the redacted payload
	Redactions  []string `json:"redactions,omitempty"` // class names removed by L1
	Answer      any      `json:"answer,omitempty"`     // the judge's answer, in System One's shape whichever API gave it (no content of ours)
	OK          bool     `json:"ok"`
	Error       string   `json:"error,omitempty"`
	// Model is the judge model that answered, as the API names it
	// (jev-1.13.0 for jev-latest; the Decisions API names none); Usage what
	// the request used. Both absent when no answer came back.
	Model string    `json:"model,omitempty"`
	Usage *JevUsage `json:"usage,omitempty"`
}

// JevUsage is what one judge request used, as the API reports it, and what
// it cost: an estimate from the judge's prices ([judge] price_input and
// price_output, else its provider's), since the API reports no cost; absent
// when no prices are known.
type JevUsage struct {
	InputTokens  int      `json:"input_tokens"`
	OutputTokens int      `json:"output_tokens"`
	Cost         *float64 `json:"cost,omitempty"`
}

// CredRegister records a credential registration. Class and Handle are
// metadata only; the secret value never appears in the log.
type CredRegister struct {
	Actor    string `json:"actor,omitempty"`
	PromptID string `json:"prompt_id,omitempty"`
	Class    string `json:"class"`
	Handle   string `json:"handle"` // the handle id
}

// SettingChange records a setting written to config.toml because a person
// chose it: Key is table.key (docs.google_upload), Value what was written,
// State "applied" or "failed" with Error. PromptID names the question it
// answered, when it was one.
type SettingChange struct {
	Actor    string `json:"actor,omitempty"`
	PromptID string `json:"prompt_id,omitempty"`
	Key      string `json:"key"`
	Value    string `json:"value"`
	State    string `json:"state"`
	Error    string `json:"error,omitempty"`
}

// CredRefresh records one refresh of an OAuth2 credential, made just before
// a use the binding table allowed (ADR 24 §3). Never a token: the class, the
// handle id, the token endpoint's host, and the new expiry or the error.
type CredRefresh struct {
	Class    string `json:"class"`
	Handle   string `json:"handle"`
	Endpoint string `json:"endpoint"`
	Expires  string `json:"expires,omitempty"` // RFC 3339, when it succeeded and the token expires
	Rotated  bool   `json:"rotated,omitempty"` // the provider issued a new refresh token
	OK       bool   `json:"ok"`
	Error    string `json:"error,omitempty"`
}

// MCPEgress records one attempt of a contained MCP server to reach a host
// through its egress proxy (ADR 23 W3b): the server, the host and port,
// and whether the proxy made the connection. Never a path or a query. A
// server that keeps trying is recorded up to a limit; the record after it
// says how many more attempts were left out.
type MCPEgress struct {
	Server  string `json:"server"`
	Method  string `json:"method,omitempty"` // CONNECT, or a plain-HTTP request's method
	Host    string `json:"host,omitempty"`
	Port    int    `json:"port,omitempty"`
	Allowed bool   `json:"allowed"`
	Reason  string `json:"reason,omitempty"`  // why it was refused or failed
	Dropped int    `json:"dropped,omitempty"` // attempts not recorded after the limit
}

// Note is a free-form annotation.
type Note struct {
	Text string `json:"text"`
}

// SessionTitle is the name a person gave a session (ADR 39). The latest one
// is the session's title; an empty one takes the name back, and the session
// is read by its first message again.
type SessionTitle struct {
	Actor string `json:"actor,omitempty"`
	Title string `json:"title"`
}

// SessionArchive records a person archiving a session or bringing it back
// (ADR 39). The latest one says whether the task list leaves it out.
type SessionArchive struct {
	Actor    string `json:"actor,omitempty"`
	Archived bool   `json:"archived"`
}

// SessionModel records the model a conversation talks to (ADR 40): the
// person chose it when the conversation opened, or another one for it since,
// and the next turn asks it. From is the model id it talked to before (empty
// at open); Model is the model id now, as the provider names it. Server names
// the [servers.NAME] it is on, Name the [models.NAME] entry it is ("control"
// for config.toml's) and URL where it is reached. Context is the window's
// recipe from here on, sized for this model, as session.resume's is. No key
// and no credential class is here: a switch changes nothing about either.
//
// Effort is the reasoning effort the requests after it ask for (ADR 41); empty
// leaves the model to its provider's default. A person may change the effort
// alone, so a record may name the model the conversation already talks to.
// Queued says the choice was made while a turn was running: it applies when
// that turn ends, and a record without Context is what says so. The record
// that applies it follows, so the last record always says what the next turn
// asks, and a conversation resumed before it was applied still has the choice.
type SessionModel struct {
	Actor   string         `json:"actor,omitempty"`
	From    string         `json:"from,omitempty"`
	Model   string         `json:"model"`
	Server  string         `json:"server,omitempty"`
	Name    string         `json:"name,omitempty"`
	URL     string         `json:"url,omitempty"`
	Effort  string         `json:"effort,omitempty"`
	Queued  bool           `json:"queued,omitempty"`
	Context *WindowContext `json:"context,omitempty"`
}

// SessionJudge records the judge a task asks, chosen as it opened or since
// (ADR 47): its server, or "" for none, with the provider, host and model
// its questions go to, so the log says where they went even after
// config.toml changes. Resuming restores it.
type SessionJudge struct {
	Actor    string `json:"actor,omitempty"`
	Server   string `json:"server"`
	Provider string `json:"provider,omitempty"`
	Host     string `json:"host,omitempty"`
	Model    string `json:"model,omitempty"`
}

// SessionDelegates records the configured models a task may hand work to
// with its delegate tool (ADR 27, amended 2026-10-09), chosen as it opened.
// Resuming restores them, those config.toml still has.
type SessionDelegates struct {
	Actor  string   `json:"actor,omitempty"`
	Models []string `json:"models"`
}

// SessionLimits records whether a task's turns stop at the turn limits
// (ADR 49): On false is a task a person let run as long as it needs, with no
// limit on tool calls, tokens, actions or time; its spend limit still
// applies. A task without one has its limits on. Resuming restores it.
type SessionLimits struct {
	Actor string `json:"actor,omitempty"`
	On    bool   `json:"on"`
}

// TurnCheckpoint records a budget decision, including cancellation and the
// unchanged hard ceiling. Text is redacted by the ordinary Record boundary.
type TurnCheckpoint struct {
	Actor      string            `json:"actor,omitempty"`
	PromptID   string            `json:"prompt_id,omitempty"`
	Checkpoint budget.Checkpoint `json:"checkpoint"`
	Choice     budget.Choice     `json:"choice"`
	Error      string            `json:"error,omitempty"`
}

// ApprovalChange records the scope and issuer of an explicit temporary grant.
// It never contains a secret, and replay never reinstalls the grant.
type ApprovalChange struct {
	Actor    string `json:"actor,omitempty"`
	PromptID string `json:"prompt_id"`
	Scope    string `json:"scope"`
	Kind     string `json:"kind"` // command or write; all for "don't ask" (every ordinary write and command)
	Prefix   string `json:"prefix"`
	// Off: the approval ends here, as "don't ask" turned off does.
	Off bool `json:"off,omitempty"`
}

// EvalRequest/EvalResult are user-origin history, never model messages.
type EvalRequest struct {
	Source string `json:"source"`
	Actor  string `json:"actor,omitempty"`
}

// EvalResult is the outcome of the EvalRequest before it.
type EvalResult struct {
	Value string `json:"value,omitempty"`
	Error string `json:"error,omitempty"`
}

// BindingChange records intent before persistence and its outcome afterwards.
// Replaying these audit records never reinstalls permission.
type BindingChange struct {
	Actor    string          `json:"actor"`
	PromptID string          `json:"prompt_id"`
	Binding  binding.Binding `json:"binding"`
	State    string          `json:"state"` // requested, applied, failed
	Error    string          `json:"error,omitempty"`
}

// WindowContext captures the recipe fixed for one session runtime. Resume may
// install a new recipe. It contains no model response or current conversation.
type WindowContext struct {
	Version         string          `json:"version"`
	System          string          `json:"system"`
	Tools           json.RawMessage `json:"tools"`
	HighWater       int             `json:"high_water"`
	LowWater        int             `json:"low_water"`
	MaxResultTokens int             `json:"max_result_tokens"`
	NumCtx          int             `json:"num_ctx"`
	KeepAlive       string          `json:"keep_alive"`
	// The headers of the task and its plan in the system prompt (window
	// recipe v6); absent means the defaults.
	TaskHeader string `json:"task_header,omitempty"`
	PlanHeader string `json:"plan_header,omitempty"`
	// MaxOutput caps each request's generated tokens (window recipe v8);
	// absent, requests carried no cap.
	MaxOutput int `json:"max_output,omitempty"`
}

// Summary is a user-authored, revocable replacement for a closed conversation
// span. SourceHash binds it to the redacted source events, not to other caches.
type Summary struct {
	From       uint64 `json:"from"`
	Through    uint64 `json:"through"`
	SourceHash string `json:"source_hash"`
	Text       string `json:"text"`
}

// SummaryDrop revokes the summary recorded at SummarySeq; the window uses
// the source events again.
type SummaryDrop struct {
	SummarySeq uint64 `json:"summary_seq"`
}

// TurnEnd retains the old note text and adds machine-readable outcomes:
// completed, stopped, hard_budget, spend_limit (the task's spend limit; no
// later turn may start), canceled or error. Older logs lack Outcome; readers
// must treat that as unknown, not as success.
type TurnEnd struct {
	Text    string `json:"text"`
	Outcome string `json:"outcome,omitempty"`
	Error   string `json:"error,omitempty"`
	// Where says where a turn that stopped short had got to, in a line: the
	// plan's marks, the files the file tools wrote, the last command and how
	// it ended. Empty for a turn that completed or was cancelled.
	Where string `json:"where,omitempty"`
}

// RunState is a durable snapshot of driver limits and disposition. The original
// deadline and maximum are preserved when explicitly resuming.
type RunState struct {
	Task     string    `json:"task"`
	Deadline time.Time `json:"deadline"`
	MaxTurns int       `json:"max_turns"`
	Turns    int       `json:"turns"`
	Status   string    `json:"status"`
	Reason   string    `json:"reason,omitempty"`
	Actor    string    `json:"actor,omitempty"`
	// Unattended: nobody answers this run's prompts, and it goes on from a
	// turn only when that turn marked a plan step done (ADR 18 §5).
	// AutoApprove: its ordinary tool confirms are answered yes. Both are
	// how the run was started or resumed; a resume sets them again.
	Unattended  bool `json:"unattended,omitempty"`
	AutoApprove bool `json:"auto_approve,omitempty"`
	// Judge: the judge answered its ordinary tool confirms.
	Judge bool `json:"judge,omitempty"`
	// Cost: the run's spend limit in US dollars, when it set its own.
	Cost float64 `json:"cost,omitempty"`
}

// RunTurn marks the start of a run's Number-th turn.
type RunTurn struct {
	Number int `json:"number"`
}

// RunCheckpoint is the answer, after turn Number, to whether the run should
// continue.
type RunCheckpoint struct {
	Number   int    `json:"number"`
	Allowed  bool   `json:"allowed"`
	Actor    string `json:"actor,omitempty"`
	PromptID string `json:"prompt_id"`
	// Reason says why an unattended run went on or stopped: the plan steps
	// the turn marked done, or that it marked none.
	Reason string `json:"reason,omitempty"`
}

// FileVersion records that a file's previous content was kept before nodwyn
// replaced it, where git is not the undo: the path, and the content's hash
// in the workspace's version store (.nodwyn/versions).
type FileVersion struct {
	Path  string `json:"path"`
	Hash  string `json:"hash"`
	Bytes int    `json:"bytes"`
}

// DocUpload is one document sent to a document service (W5): which file,
// how big, its hash, where it went and what came back. The content never
// passes through Scheme, so the guard never saw it; this record says what
// left. Never the content, never a token. Sent is false when nothing left
// the machine (the document was refused, or the credential could not be
// used); OK is true when the service made the document.
type DocUpload struct {
	Tool   string `json:"tool"`   // google-doc-upload
	Origin string `json:"origin"` // user, model or harness
	Path   string `json:"path"`   // absolute
	Type   string `json:"type"`   // the media type sent
	Bytes  int    `json:"bytes"`
	SHA256 string `json:"sha256"`
	Host   string `json:"host"`  // the destination
	Class  string `json:"class"` // the credential class that went with it
	Sent   bool   `json:"sent"`
	OK     bool   `json:"ok"`
	ID     string `json:"id,omitempty"`  // the document's id at the service
	URL    string `json:"url,omitempty"` // where the person opens it
	Error  string `json:"error,omitempty"`
}

// Attach is a file the person attached (ADR 22 §3). Text is what the model
// reads: extracted by type, redacted at ingest and capped. It is data, never
// instructions; Stored is the copy under the workspace's .nodwyn/attachments.
//
// An image (type image) has no text, and the log never holds its pixels:
// the window reads them from Stored and checks them against SHA256 when it
// builds a request. MediaType, Width and Height describe it, Tokens is what
// the window counts for it, and Destination is the host the person agreed
// it may go to, in the image's own confirm; no request to another host
// carries it.
type Attach struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Type       string   `json:"type"`  // text | pdf | docx | xlsx | pptx | image
	Bytes      int64    `json:"bytes"` // the file's size
	SHA256     string   `json:"sha256"`
	Stored     string   `json:"stored"` // path relative to the workspace root
	Pages      int      `json:"pages,omitempty"`
	Text       string   `json:"text"`
	Omitted    int      `json:"omitted,omitempty"` // bytes of extracted text left out by the cap
	Redactions []string `json:"redactions,omitempty"`
	Actor      string   `json:"actor,omitempty"`
	// An image's (window recipe v12).
	MediaType   string `json:"media_type,omitempty"` // image/png | image/jpeg | image/gif | image/webp
	Width       int    `json:"width,omitempty"`
	Height      int    `json:"height,omitempty"`
	Tokens      int    `json:"tokens,omitempty"`      // the window's estimate (window.ImageTokens)
	Destination string `json:"destination,omitempty"` // host:port of the control model it was confirmed for
	ApprovedBy  string `json:"approved_by,omitempty"` // who answered its confirm
	PromptID    string `json:"prompt_id,omitempty"`
}

// ImageGap is an attached image a request did not carry, and why: its
// stored copy is "missing", or "changed" since it was attached; the model
// asked takes no images ("no-vision"); or the request went to a host other
// than the one the image was confirmed for ("elsewhere"). The request
// carries a line saying so instead.
type ImageGap struct {
	Seq    uint64 `json:"seq"` // the attach record
	Reason string `json:"reason"`
}
