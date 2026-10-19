package rpc

import (
	"bufio"
	"context"
	"errors"
	"net"
	"sync"
	"syscall"

	"github.com/fclaude/nodwyn/internal/core"
	"github.com/fclaude/nodwyn/internal/startup"
)

// A ServeOption gives a daemon what only a person's own computer has: its
// desktop and its login item. Tests and the browser demo leave them out, or
// give a place of their own, so that neither shows a notification or writes
// a real login item.
type ServeOption func(*server)

// WithNotify shows the daemon's desktop notices with notify, when Settings'
// notify.desktop is on.
func WithNotify(notify func(title, body string) error) ServeOption {
	return func(s *server) { s.notify = notify }
}

// WithStartup offers a login item at place that runs exe (startup.read,
// startup.write).
func WithStartup(place startup.Place, exe string) ServeOption {
	return func(s *server) { s.login = &loginItem{place, exe} }
}

// Serve owns the listener and all sessions until shutdown. A client disconnect
// closes only that connection. Cancelling ctx closes connections and session
// writers, allowing another daemon (or the plain frontend) to resume the logs.
func Serve(ctx context.Context, listener *net.UnixListener, options core.Options, opts ...ServeOption) error {
	defer listener.Close()
	if err := options.Config.Validate(); err != nil {
		return err
	}
	s := &server{options: options, sessions: make(map[string]ownedSession)}
	for _, opt := range opts {
		opt(s)
	}
	s.notifyOn.Store(options.Config.Notify.Desktop)
	s.watch.asked = func(string) { s.tell(answerTitle, answerBody) }
	// Model keys saved before they were bound for their model lists get that
	// binding now. One that cannot be written leaves the table as it was: the
	// list is then refused with the binding's reason, which the window's note
	// gives, and the model's requests go on as before.
	_ = s.bindModelLists()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s.ctx = ctx
	if err := s.ensureScheduler(); err != nil {
		return err
	}
	schedulerDone := make(chan struct{})
	go s.scheduler(ctx, schedulerDone)
	workflowsDone := make(chan struct{})
	go s.workflowLoop(ctx, workflowsDone)
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
	// The sessions are closed beside the wait for the methods still running:
	// a method waiting inside a session (an image's confirm, for ten minutes)
	// ends when the session closes, and one waiting on the network ends with
	// s.ctx.
	closed := make(chan error, 1)
	go func() { closed <- s.close() }()
	workers.Wait()
	<-schedulerDone
	<-workflowsDone
	if s.schedulerLock != nil {
		defer s.schedulerLock.Close()
		defer syscall.Flock(int(s.schedulerLock.Fd()), syscall.LOCK_UN)
	}
	return errors.Join(serveErr, <-closed)
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
		// host.connect made the rest of this connection a linked host's
		// (ADR 54).
		if client.relay != nil {
			client.relay(ctx, reader)
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
