package rpc

import (
	"encoding/json"
	"net"
	"sync"
	"time"
)

// One writer serializes responses and notifications on a connection. Neither
// session delivery nor dispatch waits on socket I/O. Overflow closes the
// connection; reconnecting clients recover durable events through log.read.
type peer struct {
	actor string // authenticated by the Unix socket, not supplied by the client
	// device is set once, at hello, by a client that relays a paired device
	// (via): the web bridge. A process claiming it only limits itself.
	device bool
	ready  bool // used only by the connection's request reader
	conn   net.Conn
	out    chan any
	done   chan struct{}
	once   sync.Once
}

func newPeer(conn net.Conn) *peer {
	return &peer{conn: conn, out: make(chan any, 128), done: make(chan struct{})}
}

// refuseDevice is the refusal of a method that is for the computer nodwyn runs
// on (ADR 30), or nil for a client that is not a paired device: the one place
// a handler says that a device may not do what it asks.
func (p *peer) refuseDevice(message string) *rpcError {
	if p.device {
		return failure(-32602, message)
	}
	return nil
}
func (p *peer) close() {
	p.once.Do(func() {
		close(p.done)
		if p.conn != nil {
			p.conn.Close()
		}
	})
}
func (p *peer) enqueue(value any) bool {
	select {
	case <-p.done:
		return false
	default:
	}
	select {
	case p.out <- value:
		return true
	default:
		p.close()
		return false
	}
}
func (p *peer) write() {
	defer p.close()
	for {
		select {
		case <-p.done:
			return
		case value, ok := <-p.out:
			if !ok {
				return
			}
			raw, err := json.Marshal(value)
			if err != nil {
				return
			}
			if err := p.conn.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
				return
			}
			if _, err := p.conn.Write(append(raw, '\n')); err != nil {
				return
			}
		}
	}
}
