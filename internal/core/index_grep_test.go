package core

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/log"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

func TestIndexGrepSessionRedactionAndReadGuard(t *testing.T) {
	r := newRig(t, false, fake.Turn{ToolCalls: []fake.ToolCall{{ID: "search", Name: "index-grep", Args: map[string]any{"path": ".", "pattern": "needle"}}}}, fake.Turn{Content: "done"})
	const canary = "sk-ant-abcdefgh1234"
	os.WriteFile(filepath.Join(r.repo, "note.txt"), []byte("needle "+canary+"\n"), 0600)
	os.WriteFile(filepath.Join(r.repo, "credentials"), []byte("needle protected-value\n"), 0600)
	if err := r.s.Send("find needle"); err != nil {
		t.Fatal(err)
	}
	r.drain("n")
	raw := r.logb.String()
	if strings.Contains(raw, canary) || strings.Contains(raw, "protected-value") {
		t.Fatal("secret in log")
	}
	if len(r.srv.Requests) != 2 {
		t.Fatal("missing model continuation")
	}
	request := r.srv.Requests[1].Raw
	if bytes.Contains(request, []byte(canary)) || bytes.Contains(request, []byte("protected-value")) {
		t.Fatal("secret in provider request")
	}
	if !bytes.Contains(request, []byte("note.txt")) || !bytes.Contains(request, []byte("needle")) {
		t.Fatal("search result absent")
	}
	events, err := log.Read(bytes.NewReader(r.logb.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	sawRead := false
	for _, ev := range events {
		if ev.Kind == log.KindGuardDecision && bytes.Contains(ev.Payload, []byte(`"prim":"fs-read"`)) {
			sawRead = true
		}
	}
	if !sawRead {
		t.Fatal("nested read bypassed guard")
	}
}
