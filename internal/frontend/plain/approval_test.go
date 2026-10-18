package plain

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/binding"
	"github.com/fclaude/nodwyn/internal/frontend/rpc"
	"github.com/fclaude/nodwyn/internal/guard"
	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/test/fakes/must"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

// The question lists the answers the guard offers, each independently, as
// the TUI does: a prompt with a binding proposal and a command offers the
// binding and the session scope, not only the binding.
func TestApprovalQuestionListsWhatTheGuardOffers(t *testing.T) {
	row := binding.Binding{Class: "service", Dest: "api.test", Methods: []string{"POST"}, Positions: []string{"body"}}
	for _, c := range []struct {
		name   string
		prompt guard.Prompt
		want   string
	}{
		{"plain", guard.Prompt{Prim: "http-request"}, "allow? [y/N] "},
		{"security", guard.Prompt{Prim: "fs-write", Path: "notes/a", Security: true}, "allow? [y/N] "},
		{"protected", guard.Prompt{Prim: "fs-write", Path: ".nodwyn/allow.toml"}, "allow? [y/N] "},
		{"write", guard.Prompt{Prim: "fs-write", Path: "notes/a"}, "allow? [y/N/a=always under notes//t=all writes this turn] "},
		{"command", guard.Prompt{Prim: "shell", Argv: []string{"go", "test"}}, "allow? [y/N/a=always go test this session] "},
		{"command with a binding", guard.Prompt{Prim: "shell", Argv: []string{"go", "test"}, Binding: &row}, "allow? [y/N/b=bind this exact scope persistently and allow/a=always go test this session] "},
	} {
		if got := approvalQuestion(c.prompt.ApprovalOptions()); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// A wider answer is the explicit scope operation, attributed to whoever
// answered, not a loose answer line: the approval record names the person
// at this connection, as the daemon knows them.
func TestLineModeWiderApprovalIsAnAttributedScope(t *testing.T) {
	srv := fake.NewT(t,
		fake.Turn{ToolCalls: []fake.ToolCall{{ID: "w", Name: "fs-write", Args: map[string]any{"path": "notes/a.txt", "content": "a"}}}},
		fake.Turn{Content: "done"})
	o := testOptions(t, srv.URL)
	must.MkdirAll(t, filepath.Join(o.Repo, "notes"), 0o755)
	client, _ := testDaemon(t, o)
	id := openTask(t, client, o.Repo)
	out := runChat(t, &Chat{Client: client, ID: id, In: strings.NewReader("write a note\na\n")})
	var info rpc.SessionDetails
	if err := client.Call(context.Background(), "session.info", map[string]any{"id": id}, &info); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(info.Writes, ","); got != "notes/" {
		t.Fatalf("standing writes %q:\n%s", got, out)
	}
	for _, ev := range history(t, client, id) {
		if ev.Kind != log.KindApprovalChange {
			continue
		}
		var change log.ApprovalChange
		if err := json.Unmarshal(ev.Payload, &change); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(change.Actor, "unix:uid=") || change.Scope != "session" {
			t.Fatalf("approval record %+v", change)
		}
		return
	}
	t.Fatal("no approval record")
}
