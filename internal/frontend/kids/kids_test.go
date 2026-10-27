package kids

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fclaude/nodwyn/internal/llm"
	fakehome "github.com/fclaude/nodwyn/test/fakes/home"
	"github.com/fclaude/nodwyn/test/fakes/must"
	fakeollama "github.com/fclaude/nodwyn/test/fakes/ollama"
)

func TestMain(m *testing.M) { clean := fakehome.Isolate(); code := m.Run(); clean(); os.Exit(code) }

func storeFor(t *testing.T) *Store {
	t.Helper()
	d := t.TempDir()
	s, e := OpenStore(filepath.Join(d, "work"), filepath.Join(d, "export"))
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func TestJournalKeepsEveryAcknowledgedEditAndRecoversPreparedWrite(t *testing.T) {
	s := storeFor(t)
	p, e := s.Create("space-game")
	if e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(s.Workspace, p.ID, "index.html")
	for _, text := range []string{"first", "second"} {
		if e = s.Write(p.ID, "turn", path, []byte(text), 0600); e != nil {
			t.Fatal(e)
		}
	}
	records, e := os.ReadDir(filepath.Join(s.Export, "records"))
	if e != nil {
		t.Fatal(e)
	}
	if len(records) != 3 {
		t.Fatal(len(records))
	}
	var first Mutation
	if e = readJSON(filepath.Join(s.Export, "records", records[1].Name()), &first); e != nil {
		t.Fatal(e)
	}
	blob, e := os.ReadFile(filepath.Join(s.Export, "blobs", first.Hash))
	if e != nil || string(blob) != "first" {
		t.Fatalf("lost first edit: %q %v", blob, e)
	}
	var pending Mutation
	if e = readJSON(filepath.Join(s.Export, "records", records[2].Name()), &pending); e != nil {
		t.Fatal(e)
	}
	pending.Seq = 4
	pending.Projects[0].Version++
	b, e := json.Marshal(pending)
	if e != nil {
		t.Fatal(e)
	}
	must.WriteFile(t, filepath.Join(s.Export, "pending.json"), b, 0600)
	must.WriteFile(t, path, []byte("crash-interrupted"), 0600)
	recovered, e := OpenStore(s.Workspace, s.Export)
	if e != nil {
		t.Fatal(e)
	}
	b, e = os.ReadFile(path)
	if e != nil || string(b) != "second" || recovered.seq != 4 {
		t.Fatalf("failed recovery: %q seq %d err %v", b, recovered.seq, e)
	}
	if e = os.Remove(filepath.Join(s.Export, "records", records[1].Name())); e != nil {
		t.Fatal(e)
	}
	if _, e = OpenStore(s.Workspace, s.Export); e == nil {
		t.Fatal("accepted a journal gap")
	}
}
func TestProjectsKeepIdentityAcrossRenameAndDeletionIsJournalled(t *testing.T) {
	s := storeFor(t)
	p, e := s.Create("game")
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Change(p.ID, "rename", "new-game", "", p.Version); e != nil {
		t.Fatal(e)
	}
	q := s.Projects()[0]
	if q.ID != p.ID || q.Slug != "new-game" {
		t.Fatal(q)
	}
	if e = s.Change(p.ID, "delete", "", "", p.Version); e == nil {
		t.Fatal("deleted stale project version")
	}
	if e = s.Change(p.ID, "delete", "", "", q.Version); e != nil {
		t.Fatal(e)
	}
	if len(s.Projects()) != 0 {
		t.Fatal("not deleted")
	}
	reopened, e := OpenStore(s.Workspace, s.Export)
	if e != nil || len(reopened.Projects()) != 0 {
		t.Fatalf("bad replay %v", e)
	}
}
func TestStaticFileValidationRejectsTraversalMetadataAndExecutables(t *testing.T) {
	for _, p := range []string{"../x.html", "/x.html", ".git/config", "a/.nodwyn/config.json", "AGENTS.md", "run.sh", "a/../x.js"} {
		if validFile(p) {
			t.Fatal(p)
		}
	}
}
func TestDailyAllowanceSurvivesRestartFailedCallsAndClockRollback(t *testing.T) {
	dir := t.TempDir()
	l, e := OpenLedger(dir, 65536, 4)
	if e != nil {
		t.Fatal(e)
	}
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, l.zone)
	l.clock = func() time.Time { return now }
	l.state = allowance{Day: now.Format("2006-01-02")}
	if e = l.save(); e != nil {
		t.Fatal(e)
	}
	if _, e = l.reserve(32768, 120000); e != nil {
		t.Fatal(e)
	}
	next, e := OpenLedger(dir, 65536, 4)
	if e != nil {
		t.Fatal(e)
	}
	next.clock = func() time.Time { return now.Add(-24 * time.Hour) }
	if _, e = next.reserve(32768, 120000); e != nil {
		t.Fatal(e)
	}
	if _, e = next.reserve(1, 1); e == nil {
		t.Fatal("restart/clock rollback reset allowance")
	}
	next.clock = func() time.Time { return now.Add(24 * time.Hour) }
	if _, e = next.reserve(32768, 120000); e != nil {
		t.Fatal(e)
	}
	if e = os.Remove(next.path); e != nil {
		t.Fatal(e)
	}
	if _, e = OpenLedger(dir, 65536, 4); e == nil {
		t.Fatal("accepted missing existing accounting")
	}
}

type scripted struct {
	mu       sync.Mutex
	requests []llm.Request
	turns    []llm.Response
}

func (p *scripted) Name() string                         { return "ollama" }
func (p *scripted) Ping(context.Context) (string, error) { return "fake", nil }
func (p *scripted) Chat(_ context.Context, r llm.Request, _ func(llm.Delta)) (*llm.Response, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.requests = append(p.requests, r)
	if len(p.turns) == 0 {
		return nil, errors.New("fake exhausted")
	}
	v := p.turns[0]
	p.turns = p.turns[1:]
	v.Usage = llm.Usage{PromptTokens: 100, OutputTokens: 100}
	return &v, nil
}
func profileFor(t *testing.T) Profile {
	d := t.TempDir()
	return Profile{Child: "test-child", Host: "https://child.test", Preview: "https://child.test:8443", Public: "https://child.example", Endpoint: "http://model:11434", Model: "local-model", State: filepath.Join(d, "state"), Workspace: filepath.Join(d, "work"), Export: filepath.Join(d, "export"), Socket: filepath.Join(d, "http.sock"), DailyTokens: 2000000, DailyMinutes: 60}
}
func TestManagedTurnWritesThroughJournalAndOffersOnlyStaticTools(t *testing.T) {
	testManagedTurnWrites(t, profileFor(t))
}

// The operator may reach the workspace through a parent directory alias. The
// core's physical paths still reach the same durable journal, while links
// within a project remain subject to the managed guard.
func TestManagedTurnWritesThroughAWorkspaceAlias(t *testing.T) {
	profile := profileFor(t)
	real := t.TempDir()
	alias := filepath.Join(t.TempDir(), "workspace")
	if err := os.Symlink(real, alias); err != nil {
		t.Fatal(err)
	}
	profile.Workspace = filepath.Join(alias, "projects")
	testManagedTurnWrites(t, profile)
}

func testManagedTurnWrites(t *testing.T, profile Profile) {
	t.Helper()
	fake := &scripted{turns: []llm.Response{{Message: llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "write", Name: "fs-write", Args: json.RawMessage(`{"path":"index.html","content":"<h1>Changed</h1>"}`)}}}}, {Message: llm.Message{Role: "assistant", Content: "Your app is ready to see."}}}}
	s, e := New(profile, fake)
	if e != nil {
		t.Fatal(e)
	}
	p, e := s.store.Create("space")
	if e != nil {
		t.Fatal(e)
	}
	s.mu.Lock()
	c, e := s.session(p.ID)
	if e != nil {
		s.mu.Unlock()
		t.Fatal(e)
	}
	c.busy = true
	c.turn = "test-turn"
	e = c.session.Send("Make an app")
	s.mu.Unlock()
	if e != nil {
		t.Fatal(e)
	}
	defer c.session.Close()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		busy := c.busy
		s.mu.Unlock()
		if !busy {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	s.mu.Lock()
	busy := c.busy
	status := c.status
	s.mu.Unlock()
	if busy {
		t.Fatal("turn did not finish")
	}
	b, e := os.ReadFile(filepath.Join(s.profile.Workspace, p.ID, "index.html"))
	if e != nil || !strings.Contains(string(b), "Changed") {
		t.Fatalf("no mutation %q %v status %s", b, e, status)
	}
	records, err := os.ReadDir(filepath.Join(profile.Export, "records"))
	if err != nil {
		t.Fatal(err)
	}
	journalled := false
	for _, entry := range records {
		var mutation Mutation
		if err := readJSON(filepath.Join(profile.Export, "records", entry.Name()), &mutation); err != nil {
			t.Fatal(err)
		}
		if mutation.Op != "write" || mutation.Turn != "test-turn" {
			continue
		}
		blob, err := os.ReadFile(filepath.Join(profile.Export, "blobs", mutation.Hash))
		if err != nil || mutation.Path != "index.html" || string(blob) != "<h1>Changed</h1>" {
			t.Fatalf("journalled mutation %+v: %q %v", mutation, blob, err)
		}
		journalled = true
	}
	if !journalled {
		t.Fatal("the edit was not journalled before acknowledgement")
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.requests) != 2 {
		t.Fatalf("%d requests", len(fake.requests))
	}
	for _, r := range fake.requests {
		for _, tool := range r.Tools {
			switch tool.Name {
			case "fs-read", "fs-list", "fs-write", "fs-edit":
			default:
				t.Fatal("unsafe tool", tool.Name)
			}
		}
	}
}

func TestChildAndProjectFileQuotasAllowEditsButRefuseMoreFiles(t *testing.T) {
	s := storeFor(t)
	first, err := s.Create("one")
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Create("two")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []Project{first, second} {
		for i := range 255 {
			must.WriteFile(t, filepath.Join(s.Workspace, p.ID, fmt.Sprintf("file-%03d.txt", i)), []byte("x"), 0600)
		}
	}
	if err = s.Write(first.ID, "turn", filepath.Join(s.Workspace, first.ID, "index.html"), []byte("edited at the quota"), 0600); err != nil {
		t.Fatal("an edit at the quota was refused:", err)
	}
	if err = s.Write(first.ID, "turn", filepath.Join(s.Workspace, first.ID, "another.txt"), []byte("x"), 0600); err == nil {
		t.Fatal("project file quota was bypassed")
	}
	if _, err = s.Create("three"); err == nil {
		t.Fatal("child file quota was bypassed by a new project")
	}
	for _, path := range []string{"a space.html", "é.html", "folder/file?.js"} {
		if validFile(path) {
			t.Fatal("downstream-incompatible path accepted:", path)
		}
	}
}

func TestBoundedProviderAcceptsBufferedOllamaCallsWithoutStreamingCallback(t *testing.T) {
	upstream := fakeollama.NewT(t, fakeollama.Turn{Thinking: "Working", Content: "Hello"}, fakeollama.Turn{Content: "Again"})
	defer upstream.Close()
	ledger, err := OpenLedger(t.TempDir(), 2000000, 60)
	if err != nil {
		t.Fatal(err)
	}
	provider := NewProvider(llm.NewOllama(upstream.URL), "local-model", ledger)
	request := llm.Request{Model: "local-model", NumCtx: 32768, MaxTokens: 4096, Messages: []llm.Message{{Role: "user", Content: "hello"}}}
	response, err := provider.Chat(context.Background(), request, nil)
	if err != nil || response.Message.Content != "Hello" {
		t.Fatalf("buffered Ollama call: %+v %v", response, err)
	}
	received := ""
	response, err = provider.Chat(context.Background(), request, func(d llm.Delta) { received += d.Text })
	if err != nil || response.Message.Content != "Again" || received != "Again" {
		t.Fatalf("streaming call: %+v %q %v", response, received, err)
	}
}
