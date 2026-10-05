package rpc

import (
	"bufio"
	"context"
	"errors"
	"net"
	"sync"

	"github.com/fclaude/nodwyn/internal/core"
)

// Serve owns the listener and all sessions until shutdown. A client disconnect
// closes only that connection. Cancelling ctx closes connections and session
// writers, allowing another daemon (or the plain frontend) to resume the logs.
func Serve(ctx context.Context, listener *net.UnixListener, options core.Options) error {
	defer listener.Close()
	if err := options.Config.Validate(); err != nil {
		return err
	}
	s := &server{options: options, sessions: make(map[string]ownedSession)}
	// Model keys saved before they were bound for their model lists get that
	// binding now. One that cannot be written leaves the table as it was: the
	// list is then refused with the binding's reason, which the window's note
	// gives, and the model's requests go on as before.
	_ = s.bindModelLists()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var mu sync.Mutex
	connections := make(map[net.Conn]bool)
	var workers sync.WaitGroup
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		<-ctx.Done()
		listener.Close()
		mu.Lock()
		for c := range connections {
			c.Close()
		}
		mu.Unlock()
	}()
	var serveErr error
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() == nil {
				serveErr = err
			}
			break
		}
		mu.Lock()
		if ctx.Err() != nil {
			conn.Close()
			mu.Unlock()
			break
		}
		connections[conn] = true
		mu.Unlock()
		workers.Add(1)
		go func() {
			defer workers.Done()
			defer func() {
				conn.Close()
				mu.Lock()
				delete(connections, conn)
				mu.Unlock()
			}()
			s.connection(ctx, conn)
		}()
	}
	cancel()
	<-stopped
	workers.Wait()
	return errors.Join(serveErr, s.close())
}
func (s *server) connection(ctx context.Context, conn net.Conn) {
	reader := bufio.NewScanner(conn)
	reader.Buffer(make([]byte, 4096), maxFrame)
	actor, err := peerIdentity(conn.(*net.UnixConn))
	if err != nil {
		return
	}
	client := newPeer(conn)
	client.actor = actor
	defer s.unsubscribe(client)
	written := make(chan struct{})
	go func() { defer close(written); client.write() }()
	defer func() { client.close(); <-written }()
	for reader.Scan() {
		if ctx.Err() != nil {
			return
		}
		reply := s.frame(reader.Bytes(), client)
		if reply != nil && !client.enqueue(reply) {
			return
		}
	}
	if reader.Err() == nil {
		// A client may half-close its request stream while still reading.
		// Remove all producers before closing the queue, then flush replies.
		s.unsubscribe(client)
		close(client.out)
		<-written
	}
}
