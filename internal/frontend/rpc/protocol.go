// Package rpc is the local JSON-RPC frontend. The daemon owns sessions; a
// connection owns only its version handshake and transport, never a session.
package rpc

import (
	"bytes"
	"encoding/json"
	"reflect"
	"slices"
	"strings"

	"github.com/fclaude/nodwyn/internal/redact"
)

// ProtocolVersion is checked at hello; a client and daemon that differ
// refuse each other.
const ProtocolVersion = 1

const maxFrame = 1 << 20 // bytes per JSON-RPC frame, either direction

var methods = []string{"run.start", "run.info", "run.stop", "run.resume", "session.list", "session.watch", "session.open", "worktree.open", "worktree.diff", "worktree.review", "session.resume", "session.model.set", "session.judge.set", "session.limits.set", "session.info", "session.close", "session.rename", "session.archive", "session.delete", "session.send", "session.abort", "session.subscribe", "session.unsubscribe", "log.read", "session.answer", "session.secret", "session.approval.create", "session.binding.create", "session.approve_all", "session.eval", "session.friction", "session.policy.read", "session.policy.write", "attention.list", "notes.read", "session.notes.write", "index.status", "index.forget", "models.status", "models.offered", "models.published", "keychain.status", "keychain.create", "keychain.unlock", "keychain.passphrase", "credentials.list", "credentials.add", "credentials.replace", "credentials.remove", "credentials.bind", "credentials.authorize", "settings.read", "settings.control.write", "settings.provider.write", "settings.provider.remove", "settings.model.write", "settings.model.remove", "models.test", "settings.docs.write", "settings.write", "workspace.list", "workspace.forgotten", "workspace.active", "workspace.save", "workspace.remove", "versions.list", "versions.diff", "session.restore", "session.attach", "mcp.list", "mcp.inspect", "mcp.save", "mcp.approve", "mcp.remove", "mcp.runtime", "mcp.pull"}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	ID      json.RawMessage `json:"id"`
}
type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func failure(code int, message string) *rpcError { return &rpcError{code, message} }

// Named parameters keep requests readable. Unknown parameters are errors so a
// typo cannot silently select a default repository or session.
func params(raw json.RawMessage, dst any, names ...string) *rpcError {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	if bytes.TrimSpace(raw)[0] != '{' {
		return failure(-32602, "Expected named parameters")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return failure(-32602, "Invalid params")
	}
	for name := range fields {
		if !slices.Contains(names, name) {
			return failure(-32602, "Unknown parameter: "+name)
		}
	}
	t := reflect.TypeOf(dst).Elem()
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		tag := strings.Split(field.Tag.Get("json"), ",")
		value, exists := fields[tag[0]]
		if (!exists && !slices.Contains(tag, "omitempty")) || (exists && bytes.Equal(value, []byte("null"))) {
			return failure(-32602, "Missing or null parameter: "+tag[0])
		}
	}
	if json.Unmarshal(raw, dst) != nil {
		return failure(-32602, "Invalid params")
	}
	return nil
}

// frame accepts one JSON value per line, including JSON-RPC batches. Calls in
// a batch execute in order; notifications execute without producing a reply.
func (s *server) frame(raw []byte, client *peer) any {
	raw = bytes.TrimSpace(raw)
	if !json.Valid(raw) {
		return response{"2.0", nil, nil, failure(-32700, "Parse error")}
	}
	if raw[0] != '[' {
		return s.call(raw, client)
	}
	var batch []json.RawMessage
	_ = json.Unmarshal(raw, &batch)
	if len(batch) == 0 {
		return response{"2.0", nil, nil, failure(-32600, "Invalid Request")}
	}
	var replies []any
	for _, item := range batch {
		if r := s.call(item, client); r != nil {
			replies = append(replies, r)
		}
	}
	if len(replies) == 0 {
		return nil
	}
	return replies
}
func (s *server) call(raw []byte, client *peer) any {
	// Decode envelope names exactly; encoding/json's struct matching otherwise
	// accepts alternate casing, contrary to JSON-RPC's case-sensitive names.
	var fields map[string]json.RawMessage
	if len(raw) == 0 || raw[0] != '{' || json.Unmarshal(raw, &fields) != nil {
		return response{"2.0", nil, nil, failure(-32600, "Invalid Request")}
	}
	var req request
	if json.Unmarshal(fields["jsonrpc"], &req.JSONRPC) != nil || json.Unmarshal(fields["method"], &req.Method) != nil {
		return response{"2.0", nil, nil, failure(-32600, "Invalid Request")}
	}
	req.Params, req.ID = fields["params"], fields["id"]
	if req.JSONRPC != "2.0" || req.Method == "" || !validID(req.ID) {
		return response{"2.0", nil, nil, failure(-32600, "Invalid Request")}
	}
	result, err := s.dispatch(req, client)
	if req.ID == nil {
		return nil
	}
	// Redact application data, preserving the caller's correlation ID exactly.
	// Requests and raw errors are never written to diagnostic logs.
	if err != nil {
		err.Message = redact.Redact(err.Message, nil).Text
	} else {
		clean, _, cleanErr := redact.Payload(result, nil)
		if cleanErr != nil {
			return response{"2.0", req.ID, nil, failure(-32603, "Response redaction failed")}
		}
		result = clean
	}
	return response{"2.0", req.ID, result, err}
}
func validID(id json.RawMessage) bool {
	if len(id) == 0 || bytes.Equal(id, []byte("null")) {
		return true
	}
	var value any
	d := json.NewDecoder(bytes.NewReader(id))
	d.UseNumber()
	if d.Decode(&value) != nil {
		return false
	}
	switch value.(type) {
	case string, json.Number:
		return true
	}
	return false
}
