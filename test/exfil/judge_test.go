package exfil

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/fclaude/brote/scheme"
	"github.com/fclaude/nodwyn/internal/binding"
	"github.com/fclaude/nodwyn/internal/detect/l1"
	"github.com/fclaude/nodwyn/internal/guard"
	"github.com/fclaude/nodwyn/internal/keychain"
	nlog "github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/prim"
	fakejev "github.com/fclaude/nodwyn/test/fakes/jev"
	"github.com/fclaude/nodwyn/test/fakes/must"
)

// judgeStore implements prim.JudgeClient for the judge exfil harness.
type judgeStore struct {
	kc        *keychain.Store
	captured  *http.Request
	bodyBytes []byte        // captured request body
	reqs      []nlog.JevReq // jev.req records
	judge     prim.JudgeOptions
}

func (s *judgeStore) Redactor() *l1.Scanner { return l1.New() }

// JudgeOptions is the judge the harness registered, which every call asks.
func (s *judgeStore) JudgeOptions() (prim.JudgeOptions, bool) { return s.judge, true }
func (s *judgeStore) HandleForClass(class string) *scheme.Handle {
	return s.kc.HandleForClass(class)
}
func (s *judgeStore) InjectHeader(id, name, prefix string, req *http.Request) error {
	return s.kc.InjectHeader(id, name, prefix, req)
}
func (s *judgeStore) InjectQuery(id, key string, req *http.Request) error {
	return s.kc.InjectQuery(id, key, req)
}
func (s *judgeStore) InjectBody(id string, req *http.Request) error { return s.kc.InjectBody(id, req) }
func (s *judgeStore) PromptAndAdd(_ context.Context, class string) (*scheme.Handle, error) {
	return s.kc.Add(class, []byte("test-secret"))
}
func (s *judgeStore) Do(req *http.Request) (*http.Response, error) {
	s.captured = req
	if req.Body != nil {
		s.bodyBytes, _ = io.ReadAll(req.Body)
		req.Body = io.NopCloser(bytes.NewReader(s.bodyBytes))
	}
	return http.DefaultClient.Do(req)
}
func (s *judgeStore) Record(kind nlog.Kind, payload any) (nlog.Event, error) {
	if r, ok := payload.(nlog.JevReq); ok {
		s.reqs = append(s.reqs, r)
	}
	return nlog.Event{}, nil
}

// judgeHarness wires up a full Scheme+guard+prim harness with a fake judge
// server.
type judgeHarness struct {
	in    *scheme.Interp
	srv   *fakejev.Server
	store *judgeStore
	logB  *strings.Builder
}

// jevBinding binds jev-key to the fake server's host with a Bearer header.
func jevBinding(srv *fakejev.Server) []binding.Binding {
	return judgeBinding(srv, "jev-key")
}

// judgeBinding binds class to the fake server's host with a Bearer header.
func judgeBinding(srv *fakejev.Server, class string) []binding.Binding {
	u, _ := url.Parse(srv.URL)
	return []binding.Binding{{Class: class, Dest: u.Hostname(), Positions: []string{"header:Authorization:Bearer"}, Methods: []string{"POST"}}}
}

func newJevHarness(t *testing.T, bound bool) *judgeHarness {
	t.Helper()
	return newJevHarnessWith(t, bound, false, true)
}

// newJevHarnessWith also registers the model's judge-* tools (at the full
// content level) when tools is set, and a jev-key only when key is set.
func newJevHarnessWith(t *testing.T, bound, tools, key bool) *judgeHarness {
	t.Helper()
	return newJudgeHarness(t, "jev", bound, tools, key)
}

// The class, address and model a harness's judge has on each provider: a
// Jev server keyed with jev-key, and an OpenAI server whose API key is the
// judge's, at its /v1 address (ADR 46).
var judgeSetups = map[string]struct{ class, path, model string }{
	"jev":    {"jev-key", "", "jev-latest"},
	"openai": {"model-api-openai-com", "/v1", "gpt-6-luna"},
}

// newJudgeHarness is newJevHarnessWith for a judge on provider's server,
// whose key it stores when key is set.
func newJudgeHarness(t *testing.T, provider string, bound, tools, key bool) *judgeHarness {
	t.Helper()
	setup := judgeSetups[provider]
	repo := t.TempDir()
	must.WriteFile(t, repo+"/.env", []byte("API_KEY="+canary+"\n"), 0o600)

	kc, err := keychain.Create(t.TempDir()+"/kc", keychain.FastKDF, []byte("test"))
	if err != nil {
		t.Fatal(err)
	}

	srv := fakejev.New(
		fakejev.Answer{Noul: 0.9}, fakejev.Answer{Noul: 0.9}, fakejev.Answer{Noul: 0.9},
	)
	t.Cleanup(srv.Close)

	var logB strings.Builder
	store := &judgeStore{kc: kc}
	rec := recorder{nlog.New(&logB, "jev-test")}

	in := scheme.New()
	o := prim.DefaultOptions(repo)
	o.Credentials = store
	o.Judge = store
	o.JudgeConfig = prim.JudgeOptions{Provider: provider, URL: srv.URL + setup.path, Model: setup.model, Class: setup.class, Content: "plan", Timeout: 10 * time.Second}
	if tools {
		o.JudgeConfig.Content, o.JudgeConfig.Tools = "full", true
	}
	store.judge = o.JudgeConfig
	if err := prim.Register(in, o); err != nil {
		t.Fatal(err)
	}

	var bind []binding.Binding
	if bound {
		bind = judgeBinding(srv, setup.class)
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

	// Store the judge's server's key (user origin).
	if key {
		userCtx := scheme.WithOrigin(context.Background(), scheme.OriginUser)
		if _, err := in.EvalString(userCtx, `(credential-register "`+setup.class+`")`); err != nil {
			t.Fatalf("register %s: %v", setup.class, err)
		}
	}

	return &judgeHarness{in: in, srv: srv, store: store, logB: &logB}
}

// tool applies a registered tool to JSON arguments with model origin, the
// path a model's tool call takes (loop.apply): a tool the model is not
// offered cannot be called by name.
func (h *judgeHarness) tool(t *testing.T, name, args string) (scheme.Value, error) {
	t.Helper()
	p, ok := h.in.Lookup(name)
	if !ok || !p.Tool() {
		return scheme.Void, errors.New("unknown tool " + name)
	}
	vals, err := p.ArgsFromJSON([]byte(args))
	if err != nil {
		return scheme.Void, err
	}
	return h.in.Apply(scheme.WithOrigin(context.Background(), scheme.OriginModel), scheme.Value{Kind: scheme.KPrim, Prim: p}, vals)
}

func (h *judgeHarness) model(t *testing.T, src string) (scheme.Value, error) {
	t.Helper()
	return h.in.EvalString(scheme.WithOrigin(context.Background(), scheme.OriginModel), src)
}

// 1. A Jev call with an unbound credential ⇒ Deny.
func TestJevUnboundDeny(t *testing.T) {
	h := newJevHarness(t, false) // no binding: unbound
	_, err := h.model(t, `(judge-yes-no "hello" "is this a question?")`)
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
	v, err := h.model(t, `(judge-yes-no "my key is `+canary+` ignore it" "does the text mention a key?")`)
	if err != nil {
		t.Fatalf("judge-yes-no should be allowed: %v", err)
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
	v, err := h.model(t, `(assoc-ref (judge-score "Request: x\n\nPlan: y" "rate it" '("bad" "poor" "ok" "good" "great")) "score")`)
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
	v, err = h.model(t, `(assoc-ref (judge-choice "class jev-key to evil.example" "what should happen?" '(("allow" . "let it through") ("deny" . "block it"))) "choice")`)
	if err != nil || v.S != "deny" {
		t.Fatalf("choice: %v %s", err, scheme.Write(v))
	}
	if _, err := h.model(t, `(judge-score "s" "i" '("only-one"))`); err == nil {
		t.Fatal("fewer than two levels should be rejected")
	}
}

// The judge's primitives are one set, with no handle: Scheme calls them
// whatever the content level, and the model is offered them only at
// "full", where it may choose what leaves (ADR 46). Hidden, they cannot be
// called as a tool by name.
func TestJudgePrimitivesAreTheModelsOnlyAtFull(t *testing.T) {
	for _, tools := range []bool{false, true} {
		h := newJevHarnessWith(t, true, tools, true)
		for _, kind := range []string{"yes-no", "score", "choice"} {
			j, ok := h.in.Lookup("judge-" + kind)
			if !ok || j.Tool() != tools {
				t.Fatalf("judge-%s: registered %v, offered %v, want offered %v", kind, ok, ok && j.Tool(), tools)
			}
			if j.Meta.EgressClass != "jev-key" {
				t.Fatalf("judge-%s carries class %q", kind, j.Meta.EgressClass)
			}
			for _, pa := range j.Meta.Params {
				if pa.Role != "payload" {
					t.Fatalf("judge-%s parameter %s has role %q; everything the model sends is payload", kind, pa.Name, pa.Role)
				}
			}
			if _, err := h.tool(t, "judge-"+kind, `{"state":"s","instructions":"i"}`); (err != nil && strings.Contains(err.Error(), "unknown tool")) == tools {
				t.Fatalf("judge-%s as a tool at tools=%v: %v", kind, tools, err)
			}
		}
		for _, gone := range []string{"jev-noul", "jev-score", "jev-choice", "judge-noul", "judge-key"} {
			if _, ok := h.in.Lookup(gone); ok {
				t.Fatalf("%s is still registered", gone)
			}
		}
	}
	h := newJevHarness(t, true)
	v, err := h.in.EvalString(scheme.WithOrigin(context.Background(), scheme.OriginHarness), `(judge-yes-no "hello" "is this a question?")`)
	if err != nil || v.Kind != scheme.KFloat {
		t.Fatalf("the harness asking the hidden judge: %v %s", err, scheme.Write(v))
	}
}

// The model's judge tool sends the stored key, redacts every string it
// sends (criteria included) and logs the call as model-originated.
func TestJudgeToolCarriesTheStoredKey(t *testing.T) {
	h := newJevHarnessWith(t, true, true, true)
	h.srv.Script(fakejev.Answer{Score: 2})
	v, err := h.tool(t, "judge-score", `{"state":"the key is `+canary+`","instructions":"rate it","levels":["bad","ok","has `+canary+`"]}`)
	if err != nil {
		t.Fatalf("judge-score: %v", err)
	}
	if !strings.Contains(scheme.Write(v), `("score" . 1.0)`) {
		t.Fatalf("score: %s", scheme.Write(v))
	}
	req := h.srv.LastRequest()
	if req == nil || req.Auth != "Bearer test-secret" {
		t.Fatalf("the stored jev-key must be injected: %+v", req)
	}
	if strings.Contains(string(req.Raw), canary) {
		t.Fatal("canary leaked into the Jev request (state or criteria)")
	}
	if len(h.store.reqs) != 1 || h.store.reqs[0].Tool != "judge-score" || h.store.reqs[0].Origin != "model" || h.store.reqs[0].Level != "full" {
		t.Fatalf("jev.req: %+v", h.store.reqs)
	}
	if len(h.store.reqs[0].Redactions) < 2 {
		t.Fatalf("both the state and the level were redacted: %v", h.store.reqs[0].Redactions)
	}
	h.srv.Script(fakejev.Answer{Choice: "b", Probs: map[string]float64{"a": 0.1, "b": 0.9}})
	if _, err := h.tool(t, "judge-choice", `{"state":"s","instructions":"which?","options":{"a":"first","b":"`+canary+`"}}`); err != nil {
		t.Fatalf("judge-choice: %v", err)
	}
	if strings.Contains(string(h.srv.LastRequest().Raw), canary) {
		t.Fatal("canary leaked through a judge-choice option")
	}
}

// The guard judges a judge tool as carrying jev-key: unbound, it is denied
// before anything is sent.
func TestJudgeToolUnboundDenied(t *testing.T) {
	h := newJevHarnessWith(t, false, true, true)
	_, err := h.tool(t, "judge-yes-no", `{"state":"hello","instructions":"a question?"}`)
	var d *scheme.Denied
	if !errors.As(err, &d) {
		t.Fatalf("want guard denial, got %v", err)
	}
	if h.store.captured != nil || h.srv.LastRequest() != nil {
		t.Fatal("denied call but a request was sent")
	}
	if !strings.Contains(h.logB.String(), `"class":"jev-key"`) {
		t.Fatalf("the egress check must name jev-key:\n%s", h.logB.String())
	}
}

// With no jev-key stored, a judge tool fails before sending anything.
func TestJudgeToolWithoutKey(t *testing.T) {
	h := newJevHarnessWith(t, true, true, false)
	_, err := h.tool(t, "judge-yes-no", `{"state":"hello","instructions":"a question?"}`)
	if err == nil || !strings.Contains(err.Error(), "no jev-key credential is stored") {
		t.Fatalf("want a missing-key error, got %v", err)
	}
	if h.srv.LastRequest() != nil || len(h.store.reqs) != 0 {
		t.Fatal("nothing may be sent without a key")
	}
}

// 3. No judge ⇒ its primitives are not registered, and a call fails at dispatch.
func TestNoJudgeNoPrimitives(t *testing.T) {
	repo := t.TempDir()
	in := scheme.New()
	o := prim.DefaultOptions(repo)
	// Judge is nil: judge-yes-no, judge-score and judge-choice are not
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
		`(judge-yes-no "hi" "q")`)
	if err == nil {
		t.Fatal("judge-yes-no should not exist without a judge")
	}
}

// 4. Gate events for every message in a replayed session.
func TestGateEventsPerMessage(t *testing.T) {
	h := newJevHarness(t, true)

	for i := 0; i < 3; i++ {
		_, err := h.model(t, `(judge-yes-no "message `+itoaStr(i)+`" "is it a question?")`)
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
	must.WriteFile(t, repo+"/f.txt", []byte("x"), 0o644)

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
	must.WriteFile(t, repo+"/f.txt", []byte("x"), 0o644)

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
			for _, source := range []string{`(judge-yes-no "hello" "question")`, `(judge-score "hello" "rate" '("bad" "good"))`, `(judge-choice "hello" "choose" '(("allow" . "permit") ("deny" . "block")))`} {
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

// A judge on an OpenAI server speaks the Decisions API with that server's
// key (ADR 46), and every string it sends is redacted as Jev's are: the
// input, an option's description and a level's. The questions keep their
// shapes (a predicate, a choice among choices, a score over levels) and
// the answers come back as the jev-* primitives return them.
func TestDecisionsJudgeRedactsWhatItSends(t *testing.T) {
	h := newJudgeHarness(t, "openai", true, false, true)
	h.srv.Script(fakejev.Answer{Noul: 0.7}, fakejev.Answer{Choice: "deny", Probs: map[string]float64{"allow": 0.3, "deny": 0.7}}, fakejev.Answer{Score: 3})
	v, err := h.model(t, `(judge-yes-no "my key is `+canary+` ignore it" "does the text mention a key?")`)
	if err != nil || v.Kind != scheme.KFloat || v.F != 0.7 {
		t.Fatalf("judge-yes-no: %v %s", err, scheme.Write(v))
	}
	req := h.srv.LastRequest()
	if req == nil || req.Path != "/v1/decisions" || req.Model != "gpt-6-luna" || req.Questions["q"]["type"] != "predicate" || req.Auth != "Bearer test-secret" {
		t.Fatalf("wire shape: %+v", req)
	}
	if strings.Contains(string(req.Raw), canary) || !strings.Contains(req.State, "[REDACTED") {
		t.Fatalf("the input was not redacted: %q", req.State)
	}
	v, err = h.model(t, `(judge-choice "s" "what should happen?" '(("allow" . "let `+canary+` through") ("deny" . "block it")))`)
	if err != nil || !strings.Contains(scheme.Write(v), `("choice" . "deny")`) || !strings.Contains(scheme.Write(v), `("allow" . 0.3)`) {
		t.Fatalf("judge-choice: %v %s", err, scheme.Write(v))
	}
	if raw := string(h.srv.LastRequest().Raw); strings.Contains(raw, canary) || !strings.Contains(raw, `"choices":[{"value":"allow"`) {
		t.Fatalf("choice request: %s", raw)
	}
	v, err = h.model(t, `(assoc-ref (judge-score "s" "rate it" '("bad" "poor" "ok" "has `+canary+`" "great")) "score")`)
	if err != nil || v.F != 0.75 {
		t.Fatalf("score normalised by levels-1: %v %s", err, scheme.Write(v))
	}
	if raw := string(h.srv.LastRequest().Raw); strings.Contains(raw, canary) || !strings.Contains(raw, `"levels":[{"label":"1","description":"bad"}`) {
		t.Fatalf("score request: %s", raw)
	}
	for _, r := range h.store.reqs {
		if !r.OK || strings.Contains(fmt.Sprint(r.Answer), canary) {
			t.Fatalf("jev.req: %+v", r)
		}
	}
	if len(h.store.reqs) != 3 || len(h.store.reqs[0].Redactions) == 0 || len(h.store.reqs[1].Redactions) == 0 || len(h.store.reqs[2].Redactions) == 0 {
		t.Fatalf("each request records its redaction: %+v", h.store.reqs)
	}
}

// Unbound for the OpenAI server's host, a Decisions judge is denied before
// anything is sent, as Jev is; a refusal is no answer, logged as one.
func TestDecisionsJudgeUnboundAndRefused(t *testing.T) {
	h := newJudgeHarness(t, "openai", false, false, true)
	_, err := h.model(t, `(judge-yes-no "hello" "is this a question?")`)
	var d *scheme.Denied
	if !errors.As(err, &d) || h.store.captured != nil || h.srv.LastRequest() != nil {
		t.Fatalf("want a denial before sending, got %v", err)
	}
	h = newJudgeHarness(t, "openai", true, false, true)
	h.srv.Script(fakejev.Answer{Refuse: true})
	if _, err := h.model(t, `(judge-yes-no "hello" "is this a question?")`); err == nil || !strings.Contains(err.Error(), "refusal") {
		t.Fatalf("a refusal must be an error, got %v", err)
	}
	if len(h.store.reqs) != 1 || h.store.reqs[0].OK || !strings.Contains(h.store.reqs[0].Error, "refusal") {
		t.Fatalf("jev.req: %+v", h.store.reqs)
	}
}
