package rpc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/fclaude/nibb/internal/core"
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
	ready := false
	for reader.Scan() {
		if ctx.Err() != nil {
			return
		}
		reply := s.frame(reader.Bytes(), &ready)
		if reply == nil {
			continue
		}
		raw, err := json.Marshal(reply)
		if err != nil {
			return
		}
		if err = conn.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
			return
		}
		if _, err = conn.Write(append(raw, '\n')); err != nil {
			return
		}
	}
}
