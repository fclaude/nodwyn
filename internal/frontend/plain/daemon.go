package plain

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/fclaude/nodwyn/internal/core"
	"github.com/fclaude/nodwyn/internal/event"
	"github.com/fclaude/nodwyn/internal/frontend/rpc"
	"github.com/fclaude/nodwyn/internal/log"
)

// payloads are the events line mode shows, by kind, with the type each
// one's payload has in the daemon: the session emits these values, and the
// daemon sends them as JSON, so each is read back into the same type.
var payloads = map[string]func(json.RawMessage) (any, error){
	event.StreamText:               as[event.Text],
	event.StreamThinking:           as[event.Text],
	event.Output:                   as[event.Text],
	event.PromptCheckpoint:         as[event.Checkpoint],
	event.PromptConfirm:            as[event.Confirm],
	event.PromptSecret:             as[event.SecretPrompt],
	event.PromptChoice:             as[event.Choice],
	event.Status:                   as[event.StatusInfo],
	event.Error:                    as[event.ErrorInfo],
	string(log.KindToolCall):       as[log.ToolCallEvent],
	string(log.KindToolResult):     as[log.ToolResult],
	string(log.KindEvalResult):     as[log.EvalResult],
	string(log.KindIndexUpdate):    as[log.IndexUpdate],
	string(log.KindGuardDecision):  as[log.GuardDecision],
	string(log.KindLLMResp):        as[log.LLMResp],
	string(log.KindLLMReq):         as[log.LLMReq],
	string(log.KindSettingChange):  as[log.SettingChange],
	string(log.KindTurnEnd):        as[log.TurnEnd],
	string(log.KindBindingChange):  as[log.BindingChange],
	string(log.KindApprovalChange): as[log.ApprovalChange],
	string(log.KindUserMsg):        as[log.UserMsg],
	string(log.KindScheduleRun):    as[log.ScheduleRun],
	string(log.KindNote):           as[log.Note],
	string(log.KindAttach):         as[log.Attach],
	string(log.KindFileVersion):    as[log.FileVersion],
	string(log.KindDocUpload):      as[log.DocUpload],
	string(log.KindContextCompact): as[log.ContextCompact],
	string(log.KindJevReq):         as[log.JevReq],
	string(log.KindMCPEgress):      as[log.MCPEgress],
}

func as[T any](raw json.RawMessage) (any, error) {
	var p T
	err := json.Unmarshal(raw, &p)
	return p, err
}

// decode is a daemon event as show takes it. ok is false for a kind line
// mode does not show, which needs no payload.
func decode(kind string, seq uint64, raw json.RawMessage) (ev event.Event, ok bool, err error) {
	read, ok := payloads[kind]
	if !ok {
		return event.Event{}, false, nil
	}
	payload, err := read(raw)
	if err != nil {
		return event.Event{}, false, fmt.Errorf("the daemon sent a %s event line mode cannot read: %w", kind, err)
	}
	return event.Event{Kind: kind, Seq: seq, Payload: payload}, true, nil
}

// call is one operation on the task, on the daemon's connection.
func (c *Chat) call(method string, params map[string]any, result any) error {
	params["id"] = c.ID
	return c.Client.Call(c.ctx, method, params, result)
}

func (c *Chat) answer(prompt, reply string) error {
	return c.call("session.answer", map[string]any{"prompt_id": prompt, "reply": reply}, nil)
}

// abort stops the turn, also once the front-end's context has ended: that
// is when a turn most needs stopping.
func (c *Chat) abort() {
	_ = c.Client.Call(context.WithoutCancel(c.ctx), "session.abort", map[string]any{"id": c.ID}, nil)
}

func (c *Chat) info() (rpc.SessionDetails, error) {
	var info rpc.SessionDetails
	err := c.call("session.info", map[string]any{}, &info)
	return info, err
}

// subscribe follows the task's events and reads its history up to them,
// page by page, with the prompts waiting for an answer.
func (c *Chat) subscribe() (history []log.Event, pending []rpc.ClientEvent, err error) {
	var sub struct {
		Page    core.LogPage      `json:"page"`
		Pending []rpc.ClientEvent `json:"pending"`
	}
	if err := c.call("session.subscribe", map[string]any{"limit": 1000}, &sub); err != nil {
		return nil, nil, err
	}
	history, err = c.pages(sub.Page)
	return history, sub.Pending, err
}

// history is the task's whole log as it stands, as the TUI's :log reads it,
// so a resumed task shows the events of the runs before this one too.
func (c *Chat) history() ([]log.Event, error) {
	var first core.LogPage
	if err := c.call("log.read", map[string]any{"limit": 1000}, &first); err != nil {
		return nil, err
	}
	return c.pages(first)
}

// pages reads the pages after first up to the record first was read
// through. Each is decoded into a page of its own: decoding into the last
// one would reuse the array the history holds.
func (c *Chat) pages(first core.LogPage) ([]log.Event, error) {
	if first.Truncated {
		return nil, fmt.Errorf("the task's log has an incomplete tail; try again when its writer has finished")
	}
	events := first.Events
	for page := first; page.More; {
		var next core.LogPage
		if err := c.call("log.read", map[string]any{"after": page.Next, "through": first.Through, "limit": 1000}, &next); err != nil {
			return nil, err
		}
		if next.Next <= page.Next && next.More {
			return nil, fmt.Errorf("the task's log did not advance")
		}
		events = append(events, next.Events...)
		page = next
	}
	return events, nil
}

// relay keeps the task's events for follow and drainIdle as they come,
// any number of them while line mode waits for a line: the connection's own
// queue is bounded and closes when full, and a turn another client or the
// scheduler started goes on streaming while nobody here reads. arrived has
// a value after each, and after the connection ends, which lost says why.
func (c *Chat) relay() {
	c.arrived = make(chan struct{}, 1)
	go func() {
		for {
			ev, err := c.Client.Next(c.ctx)
			c.mu.Lock()
			if err != nil {
				c.lost = err
			} else {
				c.queue = append(c.queue, ev)
			}
			c.mu.Unlock()
			select {
			case c.arrived <- struct{}{}:
			default:
			}
			if err != nil {
				return
			}
		}
	}()
}

// take is the oldest event that has come, if one has (ok); gone says that
// none ever will, since the connection ended.
func (c *Chat) take() (ev rpc.ClientEvent, ok, gone bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.queue) > 0 {
		ev = c.queue[0]
		c.queue = c.queue[1:]
		return ev, true, false
	}
	return ev, false, c.lost != nil
}

// lostWhy is why the connection ended.
func (c *Chat) lostWhy() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lost
}
