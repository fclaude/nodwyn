package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/coder/websocket"
	"github.com/fclaude/nodwyn/internal/frontend/rpc"
)

// callerOf is who a request of device comes from, as the console's socket
// sees it when it opens.
func (b *bridge) callerOf(r *http.Request, device Device) caller {
	return caller{local: !b.remoteFor(r, device.Name), person: device.Passkey != "", touched: device.Name != "" && b.touched(device.Hash), link: device.Host}
}

// connectHost relays a console's socket for the linked host name (ADR 54):
// this computer's daemon dials the host as this computer's device there and
// carries the messages both ways, so the page keeps this console's origin
// and holds one socket per host. Whatever the host allows its device, it
// allows this socket; the bridge only checks that the browser may use this
// computer's links, which is the person's to do.
func (b *bridge) connectHost(w http.ResponseWriter, r *http.Request, device Device, token, name string) {
	ctx, cancel := context.WithCancel(b.ctx)
	defer cancel()
	refuse := func(code websocket.StatusCode, reason string) {
		if conn, err := websocket.Accept(w, r, nil); err == nil {
			conn.Close(code, closeReason(reason))
		}
	}
	if !b.allowed("host.list", b.callerOf(r, device)) {
		refuse(websocket.StatusPolicyViolation, "linked hosts are reached from this computer, or from a device signed in with your passkey")
		return
	}
	via, passkey := relay(device)
	// The daemon gives a host 15 seconds to answer.
	dialCtx, stop := context.WithTimeout(ctx, 20*time.Second)
	link, err := rpc.DialLink(dialCtx, b.socket, via, passkey, name)
	stop()
	if err != nil {
		// A host that no longer takes this computer is 1008, as a revoked
		// device is: the page stops trying. Anything else is tried again.
		var refused *rpc.LinkError
		switch {
		case errors.As(err, &refused) && refused.Refused:
			refuse(websocket.StatusPolicyViolation, refused.Message)
		case errors.As(err, &refused):
			refuse(websocket.StatusTryAgainLater, refused.Message)
		default:
			refuse(websocket.StatusTryAgainLater, "nodwyn is not running on this computer")
		}
		return
	}
	defer link.Close()
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(maxMessage)
	revoked := func() {
		conn.Close(websocket.StatusPolicyViolation, "this device was revoked")
		cancel()
	}
	b.keepAlive(ctx, conn, cancel)
	b.watchDevice(ctx, device, token, revoked)
	go func() {
		defer cancel()
		for {
			message, err := link.Next()
			if err != nil {
				var closed *rpc.LinkError
				switch {
				case errors.As(err, &closed) && closed.Refused:
					conn.Close(websocket.StatusPolicyViolation, closeReason(name+": "+closed.Message))
				case errors.As(err, &closed):
					conn.Close(websocket.StatusTryAgainLater, closeReason(name+": "+closed.Message))
				default:
					// This computer's daemon stopped, or dropped a link the
					// page fell behind on: the page connects again and reads
					// the host's tasks from their logs.
					conn.Close(websocket.StatusTryAgainLater, closeReason("the connection to "+name+" dropped"))
				}
				return
			}
			writeCtx, stop := context.WithTimeout(ctx, 10*time.Second)
			err = conn.Write(writeCtx, websocket.MessageText, message)
			stop()
			if err != nil {
				return
			}
		}
	}()
	for {
		_, message, err := conn.Read(ctx)
		if err != nil {
			return
		}
		if _, ok := b.stillPaired(device, token); !ok {
			revoked()
			return
		}
		// The daemon reads a line of at most its frame limit: a larger
		// message is answered here, as the console's own socket answers one.
		if len(message) >= maxRequest {
			var req request
			_ = json.Unmarshal(message, &req)
			if write(ctx, conn, reply(req.ID, nil, errors.New("that message is too large to send: a request is at most 1 MB"))) != nil {
				return
			}
			continue
		}
		if err := link.Send(message); err != nil {
			return
		}
	}
}

// closeReason fits reason in a WebSocket close frame, which holds at most
// 123 bytes of it.
func closeReason(reason string) string {
	if len(reason) <= 123 {
		return reason
	}
	cut := 120
	for cut > 0 && !utf8.RuneStart(reason[cut]) {
		cut--
	}
	return reason[:cut] + "…"
}
