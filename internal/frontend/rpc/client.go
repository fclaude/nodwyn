package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"
)

// These aliases expose the server's existing wire contract to frontend clients.
type (
	HelloResult    = helloResult
	SessionInfo    = sessionInfo
	SessionDetails = sessionDetails
)

// ClientEvent keeps the payload encoded until the receiving frontend interprets it.
type ClientEvent struct {
	ID      string          `json:"id"`
	Kind    string          `json:"kind"`
	Seq     uint64          `json:"seq"`
	Payload json.RawMessage `json:"payload"`
}

// Client has one socket reader, serialized writes, and bounded live delivery.
// A failed call is never retried: its side effect may already have happened.
type Client struct {
	conn    net.Conn
	writeMu sync.Mutex
	mu      sync.Mutex
	next    uint64
	waiting map[uint64]chan clientReply
	events  chan ClientEvent
	done    chan struct{}
	err     error
	once    sync.Once
}

type clientReply struct {
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

// Dial connects to the daemon's socket and checks that it speaks
// ProtocolVersion.
func Dial(ctx context.Context, socket string) (*Client, error) { return DialAs(ctx, socket, "", "") }

// DialAs is Dial for a client relaying for a named screen: via (such as
// "web:phone") is recorded after this process's identity in every answer.
// passkey, with via, names the passkey that screen's person proved themself
// with, which lifts a paired device's limits (ADR 30, amended 2026-10-17).
func DialAs(ctx context.Context, socket, via, passkey string) (*Client, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", socket)
	if err != nil {
		return nil, err
	}
	c := &Client{conn: conn, waiting: map[uint64]chan clientReply{}, events: make(chan ClientEvent, 256), done: make(chan struct{})}
	go c.read()
	var hello helloResult
	if err := c.Call(ctx, "hello", helloParams{Protocol: ProtocolVersion, Via: via, Passkey: passkey}, &hello); err != nil {
		c.Close()
		return nil, err
	}
	required := []string{"session.list", "session.open", "session.resume", "session.subscribe", "log.read", "session.send", "session.eval", "session.answer", "session.secret", "session.approval.create", "session.abort"}
	for _, name := range required {
		found := false
		for _, method := range hello.Methods {
			if method == name {
				found = true
				break
			}
		}
		if !found {
			c.Close()
			return nil, fmt.Errorf("daemon lacks %s; upgrade the daemon", name)
		}
	}
	if hello.Protocol != ProtocolVersion {
		c.Close()
		return nil, errors.New("incompatible daemon protocol")
	}
	return c, nil
}

// Close ends the connection; pending calls and Next return an error.
func (c *Client) Close() { c.fail(errors.New("connection closed")) }

func (c *Client) fail(err error) {
	c.once.Do(func() { c.mu.Lock(); c.err = err; c.mu.Unlock(); close(c.done); c.conn.Close() })
}
func (c *Client) failure() error { c.mu.Lock(); defer c.mu.Unlock(); return c.err }

// read is the connection's only reader: replies go to the waiting Call,
// notifications to the event queue. A full queue closes the connection
// rather than drop an event silently; the client reconnects and reads the
// durable history instead.
func (c *Client) read() {
	d := json.NewDecoder(c.conn)
	for {
		var frame struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      uint64          `json:"id"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params"`
			clientReply
		}
		if err := d.Decode(&frame); err != nil {
			c.fail(err)
			return
		}
		if frame.JSONRPC != "2.0" {
			c.fail(errors.New("invalid daemon response"))
			return
		}
		if frame.Method != "" {
			var ev ClientEvent
			switch frame.Method {
			case "session.event":
				if err := json.Unmarshal(frame.Params, &ev); err != nil {
					c.fail(err)
					return
				}
			case "session.closed":
				if err := json.Unmarshal(frame.Params, &ev); err != nil {
					c.fail(err)
					return
				}
				ev.Kind = "session.closed"
			default:
				continue // Additive notifications may be ignored.
			}
			select {
			case c.events <- ev:
			default:
				c.fail(errors.New("live event queue full; reconnect to recover durable history"))
				return
			}
		} else {
			c.mu.Lock()
			ch := c.waiting[frame.ID]
			delete(c.waiting, frame.ID)
			c.mu.Unlock()
			if ch != nil {
				ch <- frame.clientReply
			}
		}
	}
}

// Call sends one request and decodes the reply into result (which may be
// nil). A daemon error is returned with the method name.
func (c *Client) Call(ctx context.Context, method string, params, result any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	c.next++
	id := c.next
	reply := make(chan clientReply, 1)
	c.waiting[id] = reply
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.waiting, id); c.mu.Unlock() }()
	raw, err := json.Marshal(struct {
		JSONRPC string `json:"jsonrpc"`
		ID      uint64 `json:"id"`
		Method  string `json:"method"`
		Params  any    `json:"params"`
	}{"2.0", id, method, params})
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	defer clear(raw)
	if len(raw) >= maxFrame {
		return errors.New("request exceeds the daemon frame limit")
	}
	c.writeMu.Lock()
	if err := ctx.Err(); err != nil {
		c.writeMu.Unlock()
		return err
	}
	deadline := time.Now().Add(10 * time.Second)
	if end, ok := ctx.Deadline(); ok && end.Before(deadline) {
		deadline = end
	}
	c.conn.SetWriteDeadline(deadline)
	_, err = c.conn.Write(raw)
	c.writeMu.Unlock()
	if err != nil {
		c.fail(err)
		return err
	}
	select {
	case r := <-reply:
		if r.Error != nil {
			return fmt.Errorf("%s: %s", method, r.Error.Message)
		}
		if result != nil {
			return json.Unmarshal(r.Result, result)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return c.failure()
	}
}

// Next returns the next event from a subscribed session, in order.
func (c *Client) Next(ctx context.Context) (ClientEvent, error) {
	select {
	case ev := <-c.events:
		return ev, nil
	default:
	}
	select {
	case ev := <-c.events:
		return ev, nil
	case <-ctx.Done():
		return ClientEvent{}, ctx.Err()
	case <-c.done:
		return ClientEvent{}, c.failure()
	}
}
