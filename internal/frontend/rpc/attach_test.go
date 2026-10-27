package rpc

import (
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fclaude/nodwyn/internal/event"
	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/test/fakes/must"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

// A repeated hello is allowed while an attachment is dispatched on the same
// connection. Run under -race: readiness becomes immutable at the first hello,
// before the reader starts any attachment goroutine.
func TestRepeatedHelloBesideAttachments(t *testing.T) {
	path, _ := start(t, options(t))
	c := connect(t, path)
	c.hello()
	if err := c.conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	for i := range 30 {
		for _, req := range []map[string]any{
			{"jsonrpc": "2.0", "id": 2 * i, "method": "session.attach", "params": map[string]any{"id": "missing", "path": "/nonexistent"}},
			{"jsonrpc": "2.0", "id": 2*i + 1, "method": "hello", "params": map[string]any{"protocol": 1}},
		} {
			if err := c.enc.Encode(req); err != nil {
				t.Fatal(err)
			}
		}
	}
	seen := map[int]bool{}
	for range 60 {
		var r reply
		if err := c.dec.Decode(&r); err != nil {
			t.Fatal(err)
		}
		var id int
		if err := json.Unmarshal(r.ID, &id); err != nil {
			t.Fatal(err)
		}
		if seen[id] || id < 0 || id >= 60 {
			t.Fatalf("unexpected response id %d", id)
		}
		seen[id] = true
		if (r.Error != nil) != (id%2 == 0) {
			t.Fatalf("response %d: %+v", id, r.Error)
		}
	}
}

// A client with one connection can attach an image: the connection goes on
// reading while its session.attach waits, so the same connection answers the
// confirm it was sent as an event.
func TestImageAttachOnOneConnection(t *testing.T) {
	model := fake.New()
	t.Cleanup(model.Close)
	model.SetCapabilities("completion", "vision")
	o := options(t)
	o.Config.Control.URL, o.Config.Control.Model = model.URL, "fake-vision"
	path, _ := start(t, o)
	c := connect(t, path)
	actor := identity(t, c)
	id := opened(t, c, t.TempDir())
	subscribed(t, c, id, 0, 100)

	var b bytes.Buffer
	if err := png.Encode(&b, image.NewGray(image.Rect(0, 0, 20, 10))); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "board.png")
	must.WriteFile(t, file, b.Bytes(), 0o600)

	c.conn.SetDeadline(time.Now().Add(10 * time.Second))
	if err := c.enc.Encode(map[string]any{"jsonrpc": "2.0", "id": 7, "method": "session.attach", "params": map[string]any{"id": id, "path": file}}); err != nil {
		t.Fatal(err)
	}
	asked := prompt[event.Confirm](t, c, event.PromptConfirm)
	if !asked.Prompt.Security || asked.Prompt.Prim != "attach image" {
		t.Fatalf("confirm: %+v", asked)
	}
	if err := c.enc.Encode(map[string]any{"jsonrpc": "2.0", "id": 8, "method": "session.answer", "params": map[string]any{"id": id, "prompt_id": asked.ID, "reply": "y"}}); err != nil {
		t.Fatal(err)
	}
	// The two replies may come in either order.
	replies := map[string]reply{}
	for len(replies) < 2 {
		var r reply
		if err := c.dec.Decode(&r); err != nil {
			t.Fatalf("replies so far %v: %v", replies, err)
		}
		if r.Method == "" {
			replies[string(r.ID)] = r
		}
	}
	if r := replies["8"]; r.Error != nil {
		t.Fatalf("answer: %+v", r.Error)
	}
	r := replies["7"]
	if r.Error != nil {
		t.Fatalf("attach: %+v", r.Error)
	}
	var receipt log.Attach
	if err := json.Unmarshal(r.Result, &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Type != "image" || receipt.Width != 20 || receipt.ApprovedBy != actor {
		t.Fatalf("receipt: %s", r.Result)
	}
}

// An image's session.attach waits for its confirm, which another
// connection answers; the daemon serves other methods meanwhile.
func TestImageAttachWaitsForItsConfirm(t *testing.T) {
	model := fake.New()
	t.Cleanup(model.Close)
	model.SetCapabilities("completion", "vision")
	o := options(t)
	o.Config.Control.URL, o.Config.Control.Model = model.URL, "fake-vision"
	path, _ := start(t, o)
	watcher := connect(t, path)
	actor := identity(t, watcher)
	repo := t.TempDir()
	id := opened(t, watcher, repo)
	subscribed(t, watcher, id, 0, 100)

	img := image.NewGray(image.Rect(0, 0, 20, 10))
	var b bytes.Buffer
	png.Encode(&b, img)
	file := filepath.Join(t.TempDir(), "board.png")
	os.WriteFile(file, b.Bytes(), 0o600)

	attacher := connect(t, path)
	attacher.hello()
	done := make(chan reply, 1)
	go func() {
		attacher.conn.SetDeadline(time.Now().Add(10 * time.Second))
		attacher.enc.Encode(map[string]any{"jsonrpc": "2.0", "id": 7, "method": "session.attach", "params": map[string]any{"id": id, "path": file, "name": "board.png"}})
		var r reply
		attacher.dec.Decode(&r)
		done <- r
	}()
	asked := prompt[event.Confirm](t, watcher, event.PromptConfirm)
	if !asked.Prompt.Security || asked.Prompt.Prim != "attach image" || !strings.Contains(strings.Join(asked.Prompt.Reasons, " "), "cannot be redacted") {
		t.Fatalf("confirm: %+v", asked)
	}
	// The lifecycle lock is not held while the confirm waits.
	watcher.ok("session.info", map[string]any{"id": id})
	if r := watcher.call("session.approval.create", map[string]any{"id": id, "prompt_id": asked.ID, "scope": "session"}); r.Error == nil {
		t.Fatal("a security confirm took a standing answer")
	}
	watcher.ok("session.answer", map[string]any{"id": id, "prompt_id": asked.ID, "reply": "y"})
	r := <-done
	if r.Error != nil {
		t.Fatalf("attach: %+v", r.Error)
	}
	var receipt log.Attach
	json.Unmarshal(r.Result, &receipt)
	if receipt.Type != "image" || receipt.Width != 20 || receipt.Height != 10 || receipt.ApprovedBy != actor || receipt.Text != "" {
		t.Fatalf("receipt: %s", r.Result)
	}
}
