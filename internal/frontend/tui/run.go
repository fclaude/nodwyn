package tui

import (
	"context"
	"io"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/fclaude/nodwyn/internal/core"
	"github.com/fclaude/nodwyn/internal/frontend/rpc"
)

// Options configure the terminal client.
type Options struct {
	Core         core.Options // for a session in this process, when Socket is ""
	Socket       string       // a daemon to attach to instead
	Resume       string       // session to reopen; "" starts a new one
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
		b := &remote{client: c}
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
	var b backend
	if o.Socket != "" {
		var err error
		b, err = connect(ctx)
		if err != nil {
			return err
		}
	} else {
		b = newLocal(o.Core)
		opened = append(opened, b)
	}
	m := newModel(ctx, b, o.Core.Repo, o.Resume, o.ShowThinking)
	if o.Socket != "" {
		m.reconnect = connect
	}
	_, err := tea.NewProgram(m, tea.WithContext(ctx), tea.WithInput(o.Input), tea.WithOutput(o.Output)).Run()
	return err
}
