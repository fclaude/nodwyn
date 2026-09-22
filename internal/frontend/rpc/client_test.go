package rpc

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"
)

func TestClientDemultiplexesConcurrentCallsAndEvents(t *testing.T) {
	path, _ := start(t, options(t))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := Dial(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var info SessionDetails
	if err := c.Call(ctx, "session.open", map[string]any{"repo": t.TempDir()}, &info); err != nil {
		t.Fatal(err)
	}
	if err := c.Call(ctx, "session.subscribe", map[string]any{"id": info.ID}, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.Call(ctx, "session.eval", map[string]any{"id": info.ID, "source": "(+ 1 2)"}, nil); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			var details SessionDetails
			if err := c.Call(ctx, "session.info", map[string]any{"id": info.ID}, &details); err != nil {
				t.Error(err)
			} else if details.ID != info.ID {
				t.Error("response routed to wrong caller")
			}
		})
	}
	wg.Wait()
	found := false
	for !found {
		ev, err := c.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if ev.Kind == "eval.result" {
			var result map[string]string
			json.Unmarshal(ev.Payload, &result)
			found = result["value"] == "3"
		}
	}
	canceled, stop := context.WithCancel(ctx)
	stop()
	if err := c.Call(canceled, "session.close", map[string]any{"id": info.ID}, nil); err == nil {
		t.Fatal("canceled request sent")
	}
	if err := c.Call(ctx, "session.info", map[string]any{"id": info.ID}, &info); err != nil {
		t.Fatal("canceled close affected session", err)
	}
	c.Close()
	if err := c.Call(ctx, "session.info", map[string]any{"id": info.ID}, nil); err == nil {
		t.Fatal("closed client accepted request")
	}
}
