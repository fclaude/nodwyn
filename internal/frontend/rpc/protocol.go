// Package rpc is the local JSON-RPC frontend. The daemon owns sessions; a
// connection owns only its version handshake and transport, never a session.
package rpc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"reflect"
	"runtime/debug"
	"slices"
	"strings"

	"github.com/fclaude/nodwyn/internal/redact"
)

// ProtocolVersion is checked at hello; a client and daemon that differ
// refuse each other.
const ProtocolVersion = 1

const maxFrame = 1 << 20 // bytes per JSON-RPC frame, either direction

// methods are the names hello advertises: the dispatch table's, so a method
// cannot be implemented and not advertised, or the reverse.
var methods = slices.Sorted(maps.Keys(methodTable))

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

// waitsForPerson reports whether a frame is one request for a method that may
// wait for a person's answer, which the connection serves without holding up
// its next lines. Only session.attach does (an image's confirm); a batch
// stays in order.
func waitsForPerson(raw []byte) bool {
	var req struct {
		Method string `json:"method"`
	}
	raw = bytes.TrimSpace(raw)
	return len(raw) > 0 && raw[0] == '{' && json.Unmarshal(raw, &req) == nil && req.Method == "session.attach"
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
	result, err := s.dispatchSafely(req, client)
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

// dispatchSafely runs a method so that a bug in it fails that call, not the
// daemon and every session it holds. The diagnostic log gets the method and
// the stack, never the request or the panic's value, which may hold its data.
func (s *server) dispatchSafely(req request, client *peer) (result any, err *rpcError) {
	defer func() {
		if recover() != nil {
			fmt.Fprintf(os.Stderr, "nodwyn daemon: %s failed with an internal error\n%s", req.Method, debug.Stack())
			result, err = nil, failure(-32603, "Internal error in "+req.Method)
		}
	}()
	return s.dispatch(req, client)
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
