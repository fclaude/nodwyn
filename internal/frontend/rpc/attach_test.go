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
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

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
