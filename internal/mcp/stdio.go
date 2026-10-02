package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
)

// stdioTransport is MCP's stdio transport: one JSON-RPC message per line,
// requests on the server's standard input, responses on its standard
// output. Its standard error is the caller's to keep (capped, for
// diagnostics); nothing of it passes through here.
type stdioTransport struct {
	in  io.WriteCloser
	out io.Reader
	wmu sync.Mutex // one message at a time on in

	mu      sync.Mutex
	waiting map[string]chan *response
	done    chan struct{} // closed when the output ends
	err     error         // why it ended
	ended   func() error  // what the caller knows about why (the process's exit)
}

// NewStdio prepares a client for a server whose standard input is in and
// standard output is out. ended, when set, is asked once the output ends
// and says why (a process exit status); its error is part of the error
// every outstanding and later request gets. Close closes in.
func NewStdio(in io.WriteCloser, out io.Reader, ended func() error) *Client {
	t := &stdioTransport{in: in, out: out, waiting: map[string]chan *response{}, done: make(chan struct{}), ended: ended}
	go t.read(out)
	return &Client{t: t}
}

func (t *stdioTransport) host() string { return "" }

func (t *stdioTransport) close() error { return t.in.Close() }

// read takes the server's output a line at a time until it ends.
func (t *stdioTransport) read(out io.Reader) {
	r := bufio.NewReaderSize(out, 64<<10)
	for {
		line, err := readLine(r)
		if len(bytes.TrimSpace(line)) > 0 {
			t.dispatch(line)
		}
		if err != nil {
			t.finish(err)
			return
		}
	}
}

// readLine reads one line of at most maxBody bytes.
func readLine(r *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		chunk, err := r.ReadSlice('\n')
		if len(line)+len(chunk) > maxBody {
			return nil, fmt.Errorf("a message longer than %d bytes", maxBody)
		}
		line = append(line, chunk...)
		if !errors.Is(err, bufio.ErrBufferFull) {
			return line, err
		}
	}
}

// dispatch hands a response to the request waiting for it, answers a
// request from the server, and ignores notifications and anything that is
// not a message.
func (t *stdioTransport) dispatch(line []byte) {
	var msg struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Result json.RawMessage `json:"result"`
		Error  *rpcError       `json:"error"`
	}
	if json.Unmarshal(line, &msg) != nil {
		return
	}
	if msg.Method != "" {
		if len(msg.ID) > 0 && string(msg.ID) != "null" {
			// Not on this goroutine: a server that is not reading its
			// input must not stop nodwyn reading its output.
			go t.answer(msg.ID, msg.Method)
		}
		return
	}
	key := strings.Trim(string(msg.ID), `"`)
	t.mu.Lock()
	ch := t.waiting[key]
	delete(t.waiting, key)
	t.mu.Unlock()
	if ch != nil {
		ch <- &response{ID: msg.ID, Result: msg.Result, Error: msg.Error}
	}
}

// answer replies to a request the server sent. nodwyn declares no client
// capability, so it answers ping and refuses the rest: a server cannot ask
// it to sample the model, list roots or elicit anything.
func (t *stdioTransport) answer(id json.RawMessage, method string) {
	reply := map[string]any{"jsonrpc": "2.0", "id": id}
	if method == "ping" {
		reply["result"] = map[string]any{}
	} else {
		reply["error"] = rpcError{Code: -32601, Message: "nodwyn does not offer " + method}
	}
	body, _ := json.Marshal(reply)
	_ = t.write(body)
}

func (t *stdioTransport) finish(err error) {
	if errors.Is(err, io.EOF) {
		err = errors.New("the server's output ended")
	}
	if t.ended != nil {
		if why := t.ended(); why != nil {
			err = fmt.Errorf("%w: %w", err, why)
		}
	}
	t.mu.Lock()
	t.err = fmt.Errorf("mcp: %w", err)
	t.waiting = nil
	t.mu.Unlock()
	close(t.done)
	// Nothing more is read: a server still writing is not left waiting.
	if c, ok := t.out.(io.Closer); ok {
		_ = c.Close()
	}
}

func (t *stdioTransport) write(body []byte) error {
	t.wmu.Lock()
	defer t.wmu.Unlock()
	_, err := t.in.Write(append(body, '\n'))
	return err
}

func (t *stdioTransport) request(ctx context.Context, method string, id int64, body []byte) (*response, error) {
	key := fmt.Sprint(id)
	ch := make(chan *response, 1)
	t.mu.Lock()
	if t.waiting == nil {
		err := t.err
		t.mu.Unlock()
		return nil, err
	}
	t.waiting[key] = ch
	t.mu.Unlock()
	forget := func() {
		t.mu.Lock()
		if t.waiting != nil {
			delete(t.waiting, key)
		}
		t.mu.Unlock()
	}
	// A server that stops reading its input blocks a write; the call still
	// ends with its context.
	written := make(chan error, 1)
	go func() { written <- t.write(body) }()
	for {
		select {
		case err := <-written:
			if err != nil {
				forget()
				select {
				case <-t.done:
					return nil, t.err
				default:
				}
				return nil, fmt.Errorf("mcp: %s: %w", method, err)
			}
			written = nil
		case reply := <-ch:
			return reply, nil
		case <-t.done:
			select {
			case reply := <-ch:
				return reply, nil
			default:
			}
			return nil, t.err
		case <-ctx.Done():
			forget()
			// Tell the server to stop working on it (MCP cancellation).
			cancel, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "notifications/cancelled", "params": map[string]any{"requestId": id, "reason": ctx.Err().Error()}})
			go func() { _ = t.write(cancel) }()
			return nil, fmt.Errorf("mcp: %s: %w", method, ctx.Err())
		}
	}
}

func (t *stdioTransport) notify(_ context.Context, method string, body []byte) error {
	select {
	case <-t.done:
		return t.err
	default:
	}
	if err := t.write(body); err != nil {
		return fmt.Errorf("mcp: %s: %w", method, err)
	}
	return nil
}
