package core

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/fclaude/nibb/internal/binding"
	"github.com/fclaude/nibb/internal/config"
	"github.com/fclaude/nibb/internal/keychain"
	"github.com/fclaude/nibb/internal/llm"
	fake "github.com/fclaude/nibb/test/fakes/ollama"
)

// The userland tests hold the core to one standard: the behaviours we want
// from the agent can be written by a user in .nibb/init.scm, with nothing
// added to Go. Each test is a behaviour, written the way a user would write
// it. When one of these needs a new primitive, the core was not yet enough.

// planCycle makes the model plan before it acts: an oracle behind a key
// approves the plan, and the strategy keeps the model on it until every step
// is reported done. ORACLE_URL is replaced by the test.
const planCycle = `
(register-tool "submit-plan"
  (lambda (steps) "Plan approved. Carry out step 1, then call step-done.")
  '((doc . "For any request that needs more than one change: before editing anything, submit a plan. Each step says what to do and how to check it worked.")
    (params . (("steps" "list" "steps, each an object with do and check")))))

(register-tool "step-done"
  (lambda (step evidence) "Step recorded.")
  '((doc . "Call after finishing a plan step, with what you observed.")
    (params . (("step" "integer" "step number") ("evidence" "string" "what you observed")))))

;; The oracle is any keyed service that answers {"approve": bool, "why": text}:
;; a judge, a stronger model, a colleague's review bot.
(define (oracle-verdict steps)
  (let ((reply (http-request (credential "judge-key") "ORACLE_URL" "POST"
                             "header:Authorization:Bearer" '()
                             (json-emit (list (cons "plan" steps))))))
    (json-parse (assoc-ref reply "body"))))

(define plan '())   ; the approved steps
(define done 0)     ; steps reported done

(define (irreversible? name) (assoc-ref (primitive-info name) 'irreversible #f))

(define (handle-call c)
  (let ((name (assoc-ref c 'name))
        (args (json-parse (assoc-ref c 'args))))
    (cond ((equal? name "submit-plan")
           (let ((verdict (oracle-verdict (assoc-ref args "steps"))))
             (if (assoc-ref verdict "approve")
                 (begin (set! plan (assoc-ref args "steps"))
                        (run-tools (list c)))
                 (skip-tools (list c) (string-append "Plan rejected: " (assoc-ref verdict "why"))))))
          ((and (null? plan) (irreversible? name))
           (skip-tools (list c) "Submit a plan with submit-plan before changing anything."))
          ((equal? name "step-done")
           (set! done (+ done 1))
           (run-tools (list c)))
          (else (run-tools (list c))))))

;; Run what the model asks for; if it stops talking with steps left, tell it
;; once which step is next.
(define (agent-turn request)
  (let loop ((reply (model-turn)) (nudged #f))
    (let ((calls (assoc-ref reply 'tool-calls '())))
      (cond ((pair? calls)
             (for-each handle-call calls)
             (loop (model-turn) nudged))
            ((and (< done (length plan)) (not nudged))
             (add-message (string-append "Step " (number->string (+ done 1))
                                         " of " (number->string (length plan))
                                         " is not done yet. Continue, then call step-done."))
             (loop (model-turn) #t))))))
`

func TestUserlandPlanCycle(t *testing.T) {
	const secret = "sk-judge-canary-4711"
	var mu sync.Mutex
	var oracleCalls int
	oracle := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		oracleCalls++
		mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+secret {
			http.Error(w, "no key", http.StatusUnauthorized)
			return
		}
		var body struct{ Plan []map[string]any }
		json.NewDecoder(r.Body).Decode(&body)
		if len(body.Plan) < 2 {
			w.Write([]byte(`{"approve": false, "why": "too coarse; split it into steps, each with a check."}`))
			return
		}
		w.Write([]byte(`{"approve": true, "why": "ok"}`))
	}))
	t.Cleanup(oracle.Close)

	repo := t.TempDir()
	os.MkdirAll(filepath.Join(repo, ".nibb"), 0o755)
	os.WriteFile(filepath.Join(repo, ".nibb", "init.scm"), []byte(strings.ReplaceAll(planCycle, "ORACLE_URL", oracle.URL)), 0o644)
	kc, err := keychain.Create(filepath.Join(t.TempDir(), "kc"), keychain.FastKDF, []byte("p"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kc.Add("judge-key", []byte(secret)); err != nil {
		t.Fatal(err)
	}

	step := func(do, check string) map[string]any { return map[string]any{"do": do, "check": check} }
	call := func(id, name string, args map[string]any) fake.Turn {
		return fake.Turn{ToolCalls: []fake.ToolCall{{ID: id, Name: name, Args: args}}}
	}
	srv := fake.New(
		call("w0", "fs-write", map[string]any{"path": "a.txt", "content": "hi\n"}),
		call("p1", "submit-plan", map[string]any{"steps": []any{step("write a.txt", "read it back")}}),
		call("p2", "submit-plan", map[string]any{"steps": []any{step("write a.txt", "it exists"), step("read a.txt", "it says hi")}}),
		call("w1", "fs-write", map[string]any{"path": "a.txt", "content": "hi\n"}),
		call("d1", "step-done", map[string]any{"step": 1, "evidence": "wrote it"}),
		fake.Turn{Content: "Done."}, // stops early: step 2 is left
		call("r1", "fs-read", map[string]any{"path": "a.txt"}),
		call("d2", "step-done", map[string]any{"step": 2, "evidence": "it says hi"}),
		fake.Turn{Content: "All done."},
	)
	t.Cleanup(srv.Close)

	logb := &bytes.Buffer{}
	s, err := New(Options{
		Config: config.Default(), Repo: repo, Provider: llm.NewOllama(srv.URL), AutoApprove: true, LogWriter: logb,
		Keychain: kc,
		Bindings: binding.Table{Bind: []binding.Binding{{Class: "judge-key", Dest: "127.0.0.1", Positions: []string{"header:Authorization:Bearer"}, Methods: []string{"POST"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	r := &rig{t: t, s: s, srv: srv}
	if e := r.turn("add a.txt saying hi"); e != "" {
		t.Fatal(e)
	}

	for i, want := range map[int]string{
		1: "Submit a plan with submit-plan before changing anything.",
		2: "Plan rejected: too coarse; split it into steps, each with a check.",
		3: "Plan approved. Carry out step 1, then call step-done.",
		6: `"role":"user","content":"Step 2 of 2 is not done yet. Continue, then call step-done."`,
	} {
		if raw := string(srv.Requests[i].Raw); !strings.Contains(raw, want) {
			t.Errorf("request %d lacks %q", i, want)
		}
	}
	if len(srv.Requests) != 9 {
		t.Errorf("model requests: %d, want 9", len(srv.Requests))
	}
	if oracleCalls != 2 {
		t.Errorf("oracle calls: %d, want 2", oracleCalls)
	}
	if b, _ := os.ReadFile(filepath.Join(repo, "a.txt")); string(b) != "hi\n" {
		t.Errorf("a.txt = %q", b)
	}
	if strings.Contains(logb.String(), secret) {
		t.Error("the oracle's key reached the log")
	}
}
