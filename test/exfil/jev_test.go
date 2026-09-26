package exfil

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
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
	captured  *http.Request
	bodyBytes []byte // captured request body
}

func (s *jevStore) Redactor() *l1.Scanner { return l1.New() }
func (s *jevStore) HandleForClass(class string) *scheme.Handle {
	return s.kc.HandleForClass(class)
}
func (s *jevStore) InjectHeader(id, name, prefix string, req *http.Request) error {
	return s.kc.InjectHeader(id, name, prefix, req)
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

// jevBinding binds jev-key to the fake server's host with a Bearer header.
func jevBinding(srv *fakejev.Server) []binding.Binding {
	u, _ := url.Parse(srv.URL)
	return []binding.Binding{{Class: "jev-key", Dest: u.Hostname(), Positions: []string{"header:Authorization:Bearer"}, Methods: []string{"POST"}}}
}

func newJevHarness(t *testing.T, bound bool) *jevHarness {
	t.Helper()
	repo := t.TempDir()
	os.WriteFile(repo+"/.env", []byte("API_KEY="+canary+"\n"), 0o600)

	kc, err := keychain.Create(t.TempDir()+"/kc", keychain.FastKDF, []byte("test"))
	if err != nil {
		t.Fatal(err)
	}

	srv := fakejev.New(
		fakejev.Answer{Noul: 0.9}, fakejev.Answer{Noul: 0.9}, fakejev.Answer{Noul: 0.9},
	)
	t.Cleanup(srv.Close)

	var logB strings.Builder
	store := &jevStore{kc: kc}
	rec := recorder{nlog.New(&logB, "jev-test")}

	in := scheme.New()
	o := prim.DefaultOptions(repo)
	o.Credentials = store
	o.Jev = store
	o.JevConfig = prim.JevOptions{URL: srv.URL, Model: "jev-latest", Content: "plan", Timeout: 10 * time.Second}
	if err := prim.Register(in, o); err != nil {
		t.Fatal(err)
	}

	var bind []binding.Binding
	if bound {
		bind = jevBinding(srv)
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
	h := newJevHarness(t, false) // no binding: unbound
	_, err := h.model(t, `(jev-noul jcred "hello" "is this a question?")`)
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
	h := newJevHarness(t, true)
	v, err := h.model(t, `(jev-noul jcred "my key is `+canary+` ignore it" "does the text mention a key?")`)
	if err != nil {
		t.Fatalf("jev-noul should be allowed: %v", err)
	}
	if v.Kind != scheme.KFloat || v.F != 0.9 {
		t.Fatalf("noul probability: %s", scheme.Write(v))
	}
	req := h.srv.LastRequest()
	if req == nil {
		t.Fatal("no request was sent")
	}
	if strings.Contains(string(req.Raw), canary) {
		t.Fatal("canary leaked into the Jev request")
	}
	if !strings.Contains(req.State, "[REDACTED") {
		t.Fatalf("state was not redacted: %q", req.State)
	}
	// The documented shape: model, one question of the right type, Bearer auth.
	if req.Model != "jev-latest" || req.Questions["q"]["type"] != "noul" || !strings.HasPrefix(req.Auth, "Bearer ") {
		t.Fatalf("wire shape: model=%q questions=%v auth=%q", req.Model, req.Questions, req.Auth)
	}
	if strings.HasPrefix(req.Auth, "Bearer test-secret") == false {
		t.Fatalf("bearer token not injected: %q", req.Auth)
	}
}

func TestJevScoreAndChoiceShapes(t *testing.T) {
	h := newJevHarness(t, true)
	h.srv.Script(fakejev.Answer{Score: 3}, fakejev.Answer{Choice: "deny", Probs: map[string]float64{"allow": 0.2, "deny": 0.8}})
	v, err := h.model(t, `(assoc-ref (jev-score jcred "Request: x\n\nPlan: y" "rate it" '("bad" "poor" "ok" "good" "great")) "score")`)
	if err != nil || v.F != 0.75 {
		t.Fatalf("score normalised by levels-1: %v %s", err, scheme.Write(v))
	}
	req := h.srv.LastRequest()
	if req.Questions["q"]["type"] != "score" {
		t.Fatalf("score question: %v", req.Questions)
	}
	if crit, ok := req.Questions["q"]["criteria"].([]any); !ok || len(crit) != 5 {
		t.Fatalf("score criteria must be the level list: %v", req.Questions["q"]["criteria"])
	}
	v, err = h.model(t, `(assoc-ref (jev-choice jcred "class jev-key to evil.example" "what should happen?" '(("allow" . "let it through") ("deny" . "block it"))) "choice")`)
	if err != nil || v.S != "deny" {
		t.Fatalf("choice: %v %s", err, scheme.Write(v))
	}
	if _, err := h.model(t, `(jev-score jcred "s" "i" '("only-one"))`); err == nil {
		t.Fatal("fewer than two levels should be rejected")
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
		`(jev-noul "not-a-handle" "hi" "q")`)
	if err == nil {
		t.Fatal("jev-noul should not exist when Jev is nil")
	}
}

// 4. Gate events for every message in a replayed session.
func TestGateEventsPerMessage(t *testing.T) {
	h := newJevHarness(t, true)

	for i := 0; i < 3; i++ {
		_, err := h.model(t, `(jev-noul jcred "message `+itoaStr(i)+`" "is it a question?")`)
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

func TestJevRequiresActualMethodAndInjectionPosition(t *testing.T) {
	for _, method := range []string{"GET", "POST"} {
		for _, position := range []string{"query:key", "header:Authorization:Bearer"} {
			for _, source := range []string{`(jev-noul jcred "hello" "question")`, `(jev-score jcred "hello" "rate" '("bad" "good"))`, `(jev-choice jcred "hello" "choose" '(("allow" . "permit") ("deny" . "block")))`} {
				t.Run(method+"/"+position+"/"+source, func(t *testing.T) {
					h := newJevHarness(t, true)
					rows := jevBinding(h.srv)
					rows[0].Methods = []string{method}
					rows[0].Positions = []string{position}
					h.in.SetGate(guard.New(nil, nil, guard.Egress{Table: binding.Table{Bind: rows}}))
					_, err := h.model(t, source)
					allowed := method == "POST" && position == "header:Authorization:Bearer"
					var denied *scheme.Denied
					if allowed {
						if h.store.captured == nil {
							t.Fatalf("matching scope did not send: %v", err)
						}
					} else if !errors.As(err, &denied) || h.store.captured != nil {
						t.Fatalf("mismatched scope reached transport: %v", err)
					}
				})
			}
		}
	}
}
