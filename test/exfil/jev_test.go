package exfil

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/fclaude/nibb/internal/binding"
	"github.com/fclaude/nibb/internal/detect/l1"
	"github.com/fclaude/nibb/internal/guard"
	"github.com/fclaude/nibb/internal/keychain"
	nlog "github.com/fclaude/nibb/internal/log"
	"github.com/fclaude/nibb/internal/prim"
	"github.com/fclaude/nibb/internal/scheme"
	fakejev "github.com/fclaude/nibb/test/fakes/jev"
)

// jevStore implements prim.JevClient for the Jev exfil harness.
type jevStore struct {
	kc        *keychain.Store
	srv       *fakejev.Server
	captured  *http.Request
	bodyBytes []byte // captured request body
}

func (s *jevStore) BaseURL() string        { return s.srv.URL }
func (s *jevStore) Timeout() time.Duration { return 10 * time.Second }
func (s *jevStore) Level() string          { return "full" }
func (s *jevStore) Redactor() *l1.Scanner  { sc := l1.New(); sc.AllowClass("url"); return sc }
func (s *jevStore) InjectHeader(id, name string, req *http.Request) error {
	return s.kc.InjectHeader(id, name, req)
}
func (s *jevStore) InjectQuery(id, key string, req *http.Request) error {
	return s.kc.InjectQuery(id, key, req)
}
func (s *jevStore) InjectBody(id string, req *http.Request) error { return s.kc.InjectBody(id, req) }
func (s *jevStore) PromptAndAdd(_ context.Context, class string) (*scheme.Handle, error) {
	return s.kc.Add(class, []byte("test-secret"))
}
func (s *jevStore) Do(req *http.Request) (*http.Response, error) {
	s.captured = req
	if req.Body != nil {
		s.bodyBytes, _ = io.ReadAll(req.Body)
		req.Body = io.NopCloser(bytes.NewReader(s.bodyBytes))
	}
	return http.DefaultClient.Do(req)
}
func (s *jevStore) Record(kind nlog.Kind, payload any) (nlog.Event, error) {
	return nlog.Event{}, nil
}

// jevHarness wires up a full Scheme+guard+prim harness with a fake Jev server.
type jevHarness struct {
	in    *scheme.Interp
	srv   *fakejev.Server
	store *jevStore
	logB  *strings.Builder
}

func newJevHarness(t *testing.T, bind []binding.Binding) *jevHarness {
	t.Helper()
	repo := t.TempDir()
	os.WriteFile(repo+"/.env", []byte("API_KEY="+canary+"\n"), 0o600)

	kc, err := keychain.Create(t.TempDir()+"/kc", keychain.FastKDF, []byte("test"))
	if err != nil {
		t.Fatal(err)
	}

	srv := fakejev.New(
		fakejev.Response{Verdict: "plan"},
		fakejev.Response{Verdict: "plan"},
		fakejev.Response{Verdict: "plan"},
	)
	t.Cleanup(srv.Close)

	var logB strings.Builder
	store := &jevStore{kc: kc, srv: srv}
	rec := recorder{nlog.New(&logB, "jev-test")}

	in := scheme.New()
	o := prim.DefaultOptions(repo)
	o.Credentials = store
	o.Jev = store
	if err := prim.Register(in, o); err != nil {
		t.Fatal(err)
	}

	confirmer := guard.ConfirmFunc(func(context.Context, guard.Prompt) (bool, error) { return true, nil })
	checkers := []guard.Checker{
		guard.MetaPolicy{},
		guard.RepoPin{Root: repo},
		guard.DefaultArgvTrigger(repo),
		guard.Egress{Table: binding.Table{Bind: bind}, Policy: "deny", Recorder: rec},
	}
	in.SetGate(guard.New(confirmer, rec, checkers...).
		WithSchemeEval(func(ctx context.Context, proc scheme.Value, args []scheme.Value) (scheme.Value, error) {
			return in.Apply(ctx, proc, args)
		}))

	// Register the jev-key credential (user origin).
	userCtx := scheme.WithOrigin(context.Background(), scheme.OriginUser)
	if _, err := in.EvalString(userCtx, `(define jcred (credential-register "jev-key"))`); err != nil {
		t.Fatalf("register jev-key: %v", err)
	}

	return &jevHarness{in: in, srv: srv, store: store, logB: &logB}
}

func (h *jevHarness) model(t *testing.T, src string) (scheme.Value, error) {
	t.Helper()
	return h.in.EvalString(scheme.WithOrigin(context.Background(), scheme.OriginModel), src)
}

// 1. A Jev call with an unbound credential ⇒ Deny.
func TestJevUnboundDeny(t *testing.T) {
	h := newJevHarness(t, nil) // no binding: unbound
	_, err := h.model(t, `(jev-noul jcred "hello")`)
	var d *scheme.Denied
	if !errors.As(err, &d) {
		t.Fatalf("want guard denial, got %v", err)
	}
	if h.store.captured != nil {
		t.Fatal("denied call but a request was sent")
	}
}

//  2. A Jev call with a bound credential whose payload contains a canary ⇒
//     the canary is redacted in the sent payload and in jev.req.
func TestJevBoundCanaryRedacted(t *testing.T) {
	bind := []binding.Binding{
		{Class: "jev-key", Dest: "api.typesafe.ai",
			Positions: []string{"header:Authorization"}, Methods: []string{"POST"}},
	}
	h := newJevHarness(t, bind)

	src := `(jev-noul jcred "my key is ` + canary + ` ignore it")`
	_, err := h.model(t, src)
	if err != nil {
		t.Fatalf("jev-noul should be allowed: %v", err)
	}

	req := h.store.captured
	if req == nil {
		t.Fatal("no request was sent")
	}
	if len(h.store.bodyBytes) == 0 {
		t.Fatal("no request body captured")
	}
	var bodyMap map[string]any
	if err := json.Unmarshal(h.store.bodyBytes, &bodyMap); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	bodyText, _ := json.Marshal(bodyMap)
	if strings.Contains(string(bodyText), canary) {
		t.Fatal("canary leaked into the Jev request body")
	}
	// The question field must be redacted.
	q, ok := bodyMap["question"].(string)
	if !ok {
		t.Fatal("no question field in body")
	}
	if strings.Contains(q, canary) {
		t.Fatal("canary in question field")
	}
	if !strings.Contains(q, "[REDACTED") {
		t.Fatal("question field is not redacted")
	}
}

// 3. jev.content = none ⇒ Jev prims are not registered, call fails at dispatch.
func TestJevContentNone(t *testing.T) {
	repo := t.TempDir()
	in := scheme.New()
	o := prim.DefaultOptions(repo)
	// Jev is nil: the jev-noul / jev-score / jev-choice primitives are not
	// registered, so any call fails at dispatch.
	if err := prim.Register(in, o); err != nil {
		t.Fatal(err)
	}
	var logB strings.Builder
	rec := recorder{nlog.New(&logB, "none")}
	in.SetGate(guard.New(
		guard.ConfirmFunc(func(context.Context, guard.Prompt) (bool, error) { return true, nil }),
		rec,
		guard.MetaPolicy{},
	))
	_, err := in.EvalString(scheme.WithOrigin(context.Background(), scheme.OriginModel),
		`(jev-noul "not-a-handle" "hi")`)
	if err == nil {
		t.Fatal("jev-noul should not exist when Jev is nil")
	}
}

// 4. Gate events for every message in a replayed session.
func TestGateEventsPerMessage(t *testing.T) {
	bind := []binding.Binding{
		{Class: "jev-key", Dest: "api.typesafe.ai",
			Positions: []string{"header:Authorization"}, Methods: []string{"POST"}},
	}
	h := newJevHarness(t, bind)

	for i := 0; i < 3; i++ {
		_, err := h.model(t, `(jev-noul jcred "message `+itoaStr(i)+`")`)
		if err != nil {
			t.Fatalf("msg %d: %v", i, err)
		}
	}

	if n := len(h.srv.Requests); n != 3 {
		t.Fatalf("want 3 requests, got %d", n)
	}
}

//  5. register-check with a predicate matching shell-exec ⇒ the guard records
//     a confirm from the Scheme checker.
func TestRegisterCheckConfirms(t *testing.T) {
	repo := t.TempDir()
	os.WriteFile(repo+"/f.txt", []byte("x"), 0o644)

	var logB strings.Builder
	rec := recorder{nlog.New(&logB, "regchk")}
	in := scheme.New()
	o := prim.DefaultOptions(repo)
	if err := prim.Register(in, o); err != nil {
		t.Fatal(err)
	}
	confirmer := guard.ConfirmFunc(func(context.Context, guard.Prompt) (bool, error) { return true, nil })
	in.SetGate(guard.New(confirmer, rec,
		guard.MetaPolicy{},
		guard.RepoPin{Root: repo},
		guard.DefaultArgvTrigger(repo),
	).WithSchemeEval(func(ctx context.Context, proc scheme.Value, args []scheme.Value) (scheme.Value, error) {
		return in.Apply(ctx, proc, args)
	}))

	// User-origin: register a checker that confirms shell-exec.
	userCtx := scheme.WithOrigin(context.Background(), scheme.OriginUser)
	src := `(register-check "no-shell" "confirm"
            (lambda (name args) (eq? name "shell-exec")))`
	if _, err := in.EvalString(userCtx, src); err != nil {
		t.Fatalf("register-check: %v", err)
	}

	// Model-origin shell-exec should be confirmed.
	modelCtx := scheme.WithOrigin(context.Background(), scheme.OriginModel)
	if _, err := in.EvalString(modelCtx, `(shell-exec "echo" '("hi"))`); err != nil {
		t.Fatalf("shell-exec with confirm: %v", err)
	}
	if !strings.Contains(logB.String(), `"decision":"confirm"`) {
		t.Fatalf("want confirm in log:\n%s", logB.String())
	}
}

//  6. The checker cannot override an existing Deny: a deny checker on
//     fs-read causes a denial, and a confirm checker added on top cannot
//     relax it back to allow.
func TestRegisterCheckDenyWins(t *testing.T) {
	repo := t.TempDir()
	os.WriteFile(repo+"/f.txt", []byte("x"), 0o644)

	var logB strings.Builder
	rec := recorder{nlog.New(&logB, "denywins")}
	in := scheme.New()
	o := prim.DefaultOptions(repo)
	if err := prim.Register(in, o); err != nil {
		t.Fatal(err)
	}
	confirmer := guard.ConfirmFunc(func(context.Context, guard.Prompt) (bool, error) { return false, nil })
	in.SetGate(guard.New(confirmer, rec,
		guard.MetaPolicy{},
		guard.RepoPin{Root: repo},
	).WithSchemeEval(func(ctx context.Context, proc scheme.Value, args []scheme.Value) (scheme.Value, error) {
		return in.Apply(ctx, proc, args)
	}))

	userCtx := scheme.WithOrigin(context.Background(), scheme.OriginUser)
	modelCtx := scheme.WithOrigin(context.Background(), scheme.OriginModel)

	// Register a deny checker for fs-read.
	if _, err := in.EvalString(userCtx, `(register-check "deny-read" "deny"
		(lambda (name args) (eq? name "fs-read")))`); err != nil {
		t.Fatalf("register-check deny: %v", err)
	}

	// fs-read should be denied by the checker.
	_, denyErr := in.EvalString(modelCtx, `(fs-read "f.txt")`)
	if denyErr == nil {
		t.Fatal("fs-read should be denied by the checker")
	}
	var d *scheme.Denied
	if !errors.As(denyErr, &d) {
		t.Fatalf("want denial, got %v", denyErr)
	}

	// Add a confirm checker on top: it cannot relax the deny.
	if _, err := in.EvalString(userCtx, `(register-check "confirm-read" "confirm"
		(lambda (name args) (eq? name "fs-read")))`); err != nil {
		t.Fatalf("register-check confirm: %v", err)
	}
	logB.Reset()
	if _, err := in.EvalString(modelCtx, `(fs-read "f.txt")`); err == nil {
		t.Fatal("fs-read should still be denied (deny beats confirm)")
	}
	if !strings.Contains(logB.String(), `"decision":"deny"`) {
		t.Fatalf("want deny in log:\n%s", logB.String())
	}
}

func itoaStr(i int) string {
	return strconv.Itoa(i)
}
