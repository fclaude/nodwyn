package core

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	fake "github.com/fclaude/nibb/test/fakes/ollama"
)

func TestIndexSymbolsSessionRedaction(t *testing.T) {
	r := newRig(t, false, fake.Turn{ToolCalls: []fake.ToolCall{{ID: "outline", Name: "index-symbols", Args: map[string]any{"path": "main.go"}}}}, fake.Turn{Content: "done"})
	const canary = "AKIAABCDEFGHIJKLMNOP"
	os.WriteFile(filepath.Join(r.repo, "main.go"), []byte("package p\nfunc Safe() {}\nfunc "+canary+"() {}\n"), 0600)
	if err := r.s.Send("outline main.go"); err != nil {
		t.Fatal(err)
	}
	r.drain("n")
	if len(r.srv.Requests) != 2 {
		t.Fatal("missing continuation")
	}
	for _, surface := range [][]byte{r.logb.Bytes(), r.srv.Requests[1].Raw} {
		if bytes.Contains(surface, []byte(canary)) {
			t.Fatal("symbol canary escaped redaction")
		}
		if !bytes.Contains(surface, []byte("Safe")) {
			t.Fatal("outline missing")
		}
	}
}
