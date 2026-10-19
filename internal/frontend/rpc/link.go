package rpc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"
)

// Link is a daemon connection that carries one linked host's messages (ADR
// 54): what Send is given goes to the host as one message, and Next returns
// each message the host sends, until either end closes. The console's bridge
// opens one for each socket a page opens for that host.
type Link struct {
	conn    net.Conn
	lines   *bufio.Scanner
	writeMu sync.Mutex
}

// LinkError is the daemon's refusal of a link, or the reason a host gave for
// closing one.
type LinkError struct {
	// Refused says the host does not take this computer any more, which
	// connecting again cannot change: the person links it again.
	Refused bool
	Message string
}

func (e *LinkError) Error() string { return e.Message }

// DialLink connects to the daemon on socket as DialAs does and asks it to
// relay the linked host name.
func DialLink(ctx context.Context, socket, via, passkey, name string) (*Link, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", socket)
	if err != nil {
		return nil, err
	}
	l := &Link{conn: conn, lines: bufio.NewScanner(conn)}
	l.lines.Buffer(make([]byte, 4096), maxRelayed+1024)
	if deadline, ok := ctx.Deadline(); ok {
		conn.SetDeadline(deadline)
	}
	steps := []struct {
		method string
		params any
	}{
		{"hello", helloParams{Protocol: ProtocolVersion, Via: via, Passkey: passkey}},
		{"host.connect", hostNameParams{Name: name}},
	}
	for i, step := range steps {
		if err := l.call(uint64(i+1), step.method, step.params); err != nil {
			conn.Close()
			return nil, err
		}
	}
	conn.SetDeadline(time.Time{})
	return l, nil
}

// call sends one request and reads its reply, before the connection relays.
func (l *Link) call(id uint64, method string, params any) error {
	raw, err := json.Marshal(struct {
		JSONRPC string `json:"jsonrpc"`
		ID      uint64 `json:"id"`
		Method  string `json:"method"`
		Params  any    `json:"params"`
	}{"2.0", id, method, params})
	if err != nil {
		return err
	}
	if _, err := l.conn.Write(append(raw, '\n')); err != nil {
		return err
	}
	if !l.lines.Scan() {
		return errors.Join(errors.New("the daemon closed the connection"), l.lines.Err())
	}
	var reply struct {
		ID    uint64    `json:"id"`
		Error *rpcError `json:"error"`
	}
	if err := json.Unmarshal(l.lines.Bytes(), &reply); err != nil || reply.ID != id {
		return errors.New("invalid daemon response")
	}
	if reply.Error != nil {
		return &LinkError{Refused: reply.Error.Code == hostRefusedCode, Message: reply.Error.Message}
	}
	return nil
}

// closedPrefix starts the daemon's own notification that the host closed the
// link: the daemon writes it as the relay's last line, and a host's messages
// never start so (a console's notifications are session events, and its
// replies start with the id).
var closedPrefix = []byte(`{"jsonrpc":"2.0","method":"host.closed",`)

// Next returns the host's next message. When the host closed the link it
// returns a LinkError with the host's reason; Refused says the host revoked
// this computer.
func (l *Link) Next() ([]byte, error) {
	if !l.lines.Scan() {
		return nil, errors.Join(errors.New("the daemon closed the connection"), l.lines.Err())
	}
	line := l.lines.Bytes()
	if bytes.HasPrefix(line, closedPrefix) {
		var closed struct {
			Params hostClosed `json:"params"`
		}
		_ = json.Unmarshal(line, &closed)
		reason := closed.Params.Reason
		if reason == "" {
			reason = fmt.Sprintf("closed with %d", closed.Params.Code)
		}
		// 1008 is a console's word for a device it revoked (ADR 30).
		return nil, &LinkError{Refused: closed.Params.Code == 1008, Message: reason}
	}
	return line, nil
}

// Send sends one message to the host.
func (l *Link) Send(message []byte) error {
	if bytes.IndexByte(message, '\n') >= 0 {
		message = bytes.ReplaceAll(message, []byte("\n"), []byte(" "))
	}
	l.writeMu.Lock()
	defer l.writeMu.Unlock()
	l.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	_, err := l.conn.Write(append(message, '\n'))
	return err
}

// Close ends the link.
func (l *Link) Close() { l.conn.Close() }
