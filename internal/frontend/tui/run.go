package tui

import (
	"context"
	"io"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/fclaude/nodwyn/internal/frontend/rpc"
)

// Options configure the terminal client.
type Options struct {
	Socket string // the daemon that hosts the sessions (ADR 30)
	// Owned is set when this process serves that daemon: quitting then
	// ends its tasks rather than leaving them running.
	Owned        bool
	Repo         string // the folder new tasks start in
	Resume       string // session to reopen; "" starts a new one
	Worktree     bool   // start the new one in a git worktree of Repo (ADR 34)
	ShowThinking bool
	Input        io.Reader
	Output       io.Writer
}

// Run shows the terminal client until the user quits or ctx ends.
func Run(ctx context.Context, o Options) error {
	ctx, cancel := context.WithCancel(ctx)
	// Keep cleanup independent of which model message wins a race with quitting.
	// A newly connected backend is either registered here or immediately closed.
	var mu sync.Mutex
	var opened []backend
	closing := false
	connect := func(ctx context.Context) (backend, error) {
		dialCtx, stop := context.WithTimeout(ctx, 30*time.Second)
		defer stop()
		c, err := rpc.Dial(dialCtx, o.Socket)
		if err != nil {
			return nil, err
		}
		b := &remote{client: c, socket: o.Socket}
		mu.Lock()
		defer mu.Unlock()
		if closing {
			b.Close()
			return nil, context.Canceled
		}
		opened = append(opened, b)
		return b, nil
	}
	defer func() {
		cancel()
		mu.Lock()
		closing = true
		backends := append([]backend(nil), opened...)
		mu.Unlock()
		for _, b := range backends {
			b.Close()
		}
	}()
	b, err := connect(ctx)
	if err != nil {
		return err
	}
	m := newModel(ctx, b, o.Repo, o.Resume, o.ShowThinking)
	m.worktree, m.owned = o.Worktree, o.Owned
	m.reconnect = connect
	_, err = tea.NewProgram(m, tea.WithContext(ctx), tea.WithInput(o.Input), tea.WithOutput(o.Output)).Run()
	return err
}
