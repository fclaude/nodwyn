package exfil

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/binding"
	"github.com/fclaude/nodwyn/internal/guard"
	"github.com/fclaude/nodwyn/internal/keychain"
	nlog "github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/prim"
	"github.com/fclaude/nodwyn/internal/scheme"
)

// credSecret is the test credential value, deliberately distinct from the
// canary so we can assert it reaches the egress request header (the bound
// path works) while the canary appears nowhere (the exfil path is blocked).
const credSecret = "sk-test-cred-XYZ123"

// egressStore implements prim.CredentialStore for the exfil harness. It
// bypasses the front-end prompt (PromptAndAdd writes straight into the
// keychain) and captures the outgoing request for assertions.
type egressStore struct {
	kc       *keychain.Store
	captured *http.Request
}

func (s *egressStore) PromptAndAdd(_ context.Context, class string) (*scheme.Handle, error) {
	return s.kc.Add(class, []byte(credSecret))
}
func (s *egressStore) HandleForClass(class string) *scheme.Handle {
	return s.kc.HandleForClass(class)
}
func (s *egressStore) InjectHeader(id, name, prefix string, req *http.Request) error {
	return s.kc.InjectHeader(id, name, prefix, req)
}
func (s *egressStore) InjectQuery(id, key string, req *http.Request) error {
	return s.kc.InjectQuery(id, key, req)
}
func (s *egressStore) InjectBody(id string, req *http.Request) error {
	return s.kc.InjectBody(id, req)
}
func (s *egressStore) Do(req *http.Request) (*http.Response, error) {
	s.captured = req
	return &http.Response{StatusCode: 200, Body: http.NoBody, Header: http.Header{}}, nil
}

// egressCase is one binding scenario: a (http-request ...) call with the
// given method/position/dest, run against the given binding table.
type egressCase struct {
	name     string
	method   string
	position string
	dest     string
	bind     []binding.Binding
	expect   string // "allow" or "deny"
	wantHdr  bool   // the captured request must carry the credential header
}

func TestEgressBinding(t *testing.T) {
	cases := []egressCase{
		{
			name:     "unbound-table-deny",
			method:   "POST",
			position: "header:Authorization",
			dest:     "https://api.openai.com/v1/chat",
			bind:     nil, // empty table: nothing is bound
			expect:   "deny",
		},
		{
			name:     "bound-allow",
			method:   "POST",
			position: "header:Authorization",
			dest:     "https://api.openai.com/v1/chat",
			bind: []binding.Binding{
				{Class: "openai-key", Dest: "api.openai.com",
					Positions: []string{"header:Authorization"}, Methods: []string{"POST"}},
			},
			expect:  "allow",
			wantHdr: true,
		},
		{
			name:     "wrong-method-deny",
			method:   "DELETE",
			position: "header:Authorization",
			dest:     "https://api.openai.com/v1/chat",
			bind: []binding.Binding{
				{Class: "openai-key", Dest: "api.openai.com",
					Positions: []string{"header:Authorization"}, Methods: []string{"POST"}},
			},
			expect: "deny",
		},
		{
			name:     "wrong-position-deny",
			method:   "POST",
			position: "body",
			dest:     "https://api.openai.com/v1/chat",
			bind: []binding.Binding{
				{Class: "openai-key", Dest: "api.openai.com",
					Positions: []string{"header:Authorization"}, Methods: []string{"POST"}},
			},
			expect: "deny",
		},
		{
			name:     "wrong-host-deny",
			method:   "POST",
			position: "header:Authorization",
			dest:     "https://api.typesafe.ai/v1/systemone",
			bind: []binding.Binding{
				{Class: "openai-key", Dest: "api.openai.com",
					Positions: []string{"header:Authorization"}, Methods: []string{"POST"}},
			},
			expect: "deny",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir() + "/repo"
			if err := os.MkdirAll(repo, 0o755); err != nil {
				t.Fatal(err)
			}
			// A canary that must never reach the egress request or the log.
			os.WriteFile(repo+"/.env", []byte("API_KEY="+canary+"\n"), 0o600)
			t.Setenv("CANARY_SECRET", canary)

			var logBuf, outBuf strings.Builder
			kc, err := keychain.Create(t.TempDir()+"/kc", keychain.FastKDF, []byte("test-pass"))
			if err != nil {
				t.Fatal(err)
			}
			st := &egressStore{kc: kc}

			in := scheme.New(scheme.WithOutput(&outBuf))
			o := prim.DefaultOptions(repo)
			o.Credentials = st
			if err := prim.Register(in, o); err != nil {
				t.Fatal(err)
			}
			rec := recorder{nlog.New(&logBuf, "egress-test")}
			confirmer := guard.ConfirmFunc(func(context.Context, guard.Prompt) (bool, error) {
				return false, nil
			})
			tbl := binding.Table{Bind: tc.bind}
			in.SetGate(guard.New(confirmer, rec,
				guard.MetaPolicy{},
				guard.RepoPin{Root: repo},
				guard.DefaultArgvTrigger(repo),
				guard.Egress{Table: tbl, Policy: "deny", Recorder: rec},
			))

			// The user registers the credential (user origin: registration is
			// refused from a model-originated call), then the model's call
			// uses the handle. PromptAndAdd is stubbed to skip the prompt.
			userCtx := scheme.WithOrigin(context.Background(), scheme.OriginUser)
			if _, err := in.EvalString(userCtx, `(define cred (credential-register "openai-key"))`); err != nil {
				t.Fatalf("register: %v", err)
			}
			src := "(http-request cred \"" + tc.dest + "\" \"" + tc.method +
				"\" \"" + tc.position + "\" '() \"\")"
			ctx := scheme.WithOrigin(context.Background(), scheme.OriginModel)
			v, err := in.EvalString(ctx, src)

			got := "allow"
			var d *scheme.Denied
			if err != nil {
				if !errors.As(err, &d) {
					t.Fatalf("expected a guard denial, got: %v", err)
				}
				got = "deny"
			}
			if got != tc.expect {
				t.Errorf("decision: got %s, want %s (err=%v)", got, tc.expect, err)
			}

			// The canary must never leak into the log, output, or result.
			for what, text := range map[string]string{
				"log":    logBuf.String(),
				"output": outBuf.String(),
				"result": scheme.Write(v),
			} {
				if strings.Contains(text, canary) {
					t.Errorf("canary leaked into the %s", what)
				}
			}

			if tc.wantHdr {
				if st.captured == nil {
					t.Fatal("allowed call but no HTTP request was captured")
				}
				if got := st.captured.Header.Get("Authorization"); got != credSecret {
					t.Errorf("Authorization header: got %q, want %q", got, credSecret)
				}
			}
			if !tc.wantHdr && st.captured != nil {
				t.Errorf("denied call but a request was captured: %+v", st.captured.Header)
			}
		})
	}
}

// TestCredentialRegisterRefusesModelOrigin: a user tool wrapping
// credential-register must not let the model put a secret prompt in front
// of the user.
func TestCredentialRegisterRefusesModelOrigin(t *testing.T) {
	kc, err := keychain.Create(t.TempDir()+"/kc", keychain.FastKDF, []byte("p"))
	if err != nil {
		t.Fatal(err)
	}
	in := scheme.New()
	o := prim.DefaultOptions(t.TempDir())
	o.Credentials = &egressStore{kc: kc}
	if err := prim.Register(in, o); err != nil {
		t.Fatal(err)
	}
	_, err = in.EvalString(scheme.WithOrigin(context.Background(), scheme.OriginModel), `(credential-register "x")`)
	if err == nil || !strings.Contains(err.Error(), "model-originated") {
		t.Fatalf("model-origin registration should be refused: %v", err)
	}
	if kc.Count() != 0 {
		t.Fatal("a credential was stored")
	}
	if _, err := in.EvalString(scheme.WithOrigin(context.Background(), scheme.OriginUser), `(credential-register "x")`); err != nil {
		t.Fatalf("user-origin registration: %v", err)
	}
}

// TestCredentialLookupRefusesModelOrigin: Scheme may look up the handle of a
// stored key (so a strategy can call a keyed judge or model), but a
// model-originated evaluation may not, and an unknown class is #f.
func TestCredentialLookupRefusesModelOrigin(t *testing.T) {
	kc, err := keychain.Create(t.TempDir()+"/kc", keychain.FastKDF, []byte("p"))
	if err != nil {
		t.Fatal(err)
	}
	stored, err := kc.Add("judge-key", []byte(credSecret))
	if err != nil {
		t.Fatal(err)
	}
	in := scheme.New()
	o := prim.DefaultOptions(t.TempDir())
	o.Credentials = &egressStore{kc: kc}
	if err := prim.Register(in, o); err != nil {
		t.Fatal(err)
	}
	if _, err := in.EvalString(scheme.WithOrigin(context.Background(), scheme.OriginModel), `(credential "judge-key")`); err == nil || !strings.Contains(err.Error(), "model-originated") {
		t.Fatalf("model-origin lookup should be refused: %v", err)
	}
	for _, origin := range []scheme.Origin{scheme.OriginUser, scheme.OriginHarness} {
		v, err := in.EvalString(scheme.WithOrigin(context.Background(), origin), `(credential "judge-key")`)
		if err != nil || v.Kind != scheme.KHandle || v.H.ID != stored.ID {
			t.Fatalf("%s lookup: %v %v", origin, v, err)
		}
		if strings.Contains(scheme.Write(v), credSecret) {
			t.Fatal("a handle printed its secret")
		}
	}
	if v, _ := in.EvalString(context.Background(), `(credential "no-such-key")`); v.Kind != scheme.KBool || v.IsTrue() {
		t.Fatalf("unknown class: %v", v)
	}
	// Without a keychain there is never a handle, and no error either.
	bare := scheme.New()
	if err := prim.Register(bare, prim.DefaultOptions(t.TempDir())); err != nil {
		t.Fatal(err)
	}
	if v, err := bare.EvalString(context.Background(), `(credential "judge-key")`); err != nil || v.IsTrue() {
		t.Fatalf("no keychain: %v %v", v, err)
	}
}
