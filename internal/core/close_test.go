package core

import (
	"testing"
	"time"

	"github.com/fclaude/nodwyn/internal/event"
	"github.com/fclaude/nodwyn/internal/llm"
	"github.com/fclaude/nodwyn/internal/log"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

func TestCloseWaitsForFinalIdleDelivery(t *testing.T) {
	model := fake.NewT(t, fake.Turn{Content: "finished"})
	defer model.Close()
	o := diskOptions(t)
	o.Provider = llm.NewOllama(model.URL)
	o.EventBuffer = 1
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	// Always unblock a failed test before Close waits for the turn goroutine.
	defer func() {
		go func() {
			for range s.Events() {
			}
		}()
		s.Close()
	}()
	if err := s.Send("hello"); err != nil {
		t.Fatal(err)
	}
	timeout := time.After(5 * time.Second)
	for {
		select {
		case ev := <-s.Events():
			if ev.Kind == string(log.KindLLMResp) {
				goto responded
			}
		case <-timeout:
			t.Fatal("model did not respond")
		}
	}
responded:
	for s.Busy() {
		select {
		case <-timeout:
			t.Fatal("turn did not finish")
		case <-time.After(time.Millisecond):
		}
	}
	// The one-slot channel now contains turn.end; idle is blocked behind it.
	// Busy is false but closing the channel at this point would race that send.
	closed := make(chan error, 1)
	go func() { closed <- s.Close() }()
	for {
		s.mu.Lock()
		closing := s.closing
		s.mu.Unlock()
		if closing {
			break
		}
		select {
		case <-timeout:
			t.Fatal("close did not start")
		case <-time.After(time.Millisecond):
		}
	}
	select {
	case err := <-closed:
		t.Fatal("closed before final delivery", err)
	default:
	}
	if err := s.Send("too late"); err == nil {
		t.Fatal("accepted turn during close")
	}
	if ev := <-s.Events(); ev.Kind != string(log.KindTurnEnd) {
		t.Fatal("missing turn.end", ev)
	}
	if ev := <-s.Events(); ev.Kind != event.Status || ev.Payload.(event.StatusInfo).State != "idle" {
		t.Fatal("missing final idle", ev)
	}
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-timeout:
		t.Fatal("close did not finish")
	}
	if _, open := <-s.Events(); open {
		t.Fatal("event channel remains open")
	}
	if err := s.Close(); err != nil {
		t.Fatal("repeated close", err)
	}
}
