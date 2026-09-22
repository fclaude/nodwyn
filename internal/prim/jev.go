// Jev egress primitives (ADR 19/20): jev-noul, jev-score, jev-choice.
//
// Jev is the user's external model service — the "noul gate". A user message
// arrives at the gate: if it needs a plan, the plan is sent to Jev (redacted
// first), the gate asks for the noul verdict, and on approve the turn
// proceeds; on revision the model re-plans (bounded). A plan is always
// visible to the user and the log, whatever the verdict.
//
// These three primitives are the only Scheme→Jev surface (ADR 20). They are
// Egress prims (bytes leave the machine for the Jev service) and are
// guarded like every other egress: the guard runs first, then the handler.
// The handler:
//
//  1. reads the credential handle from the first argument;
//  2. redacts the text payload with the L1 scanner (ADR 19);
//  3. JSON-encodes the payload and POSTs it to the Jev endpoint;
//  4. records a jev.req log event (level, bytes, payload hash, redaction
//     classes, score) — never the raw payload;
//  5. returns the decoded response as a Scheme value.
//
// The handlers do not own the transport, the redactor or the log: they call
// the JevClient interface the core implements, keeping the session's
// configuration (endpoint, timeout, content level) and logging in one place.
package prim

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/fclaude/nibb/internal/detect/l1"
	nlog "github.com/fclaude/nibb/internal/log"
	"github.com/fclaude/nibb/internal/redact"
	"github.com/fclaude/nibb/internal/scheme"
)

// JevClient is what the Jev primitives need from the core session. The core
// implements it: it owns the Jev configuration (endpoint, timeout, content
// level), the L1 scanner, the HTTP transport, and the log. The interface
// keeps prim decoupled from core (no import cycle).
type JevClient interface {
	BaseURL() string
	Timeout() time.Duration
	Level() string
	Redactor() *l1.Scanner
	// InjectHeader sets the credential id as the request header name.
	InjectHeader(id, name string, req *http.Request) error
	Do(req *http.Request) (*http.Response, error)
	Record(kind nlog.Kind, payload any) (nlog.Event, error)
}

// jevEndpoint is the fixed Jev endpoint (ADR 20). The base URL is
// configurable in config.toml for test environments; the default is
// https://api.typesafe.ai.
const (
	jevDefaultBase = "https://api.typesafe.ai"
	jevPath        = "/v1/systemone"
)

// jevHost is the logical destination of every Jev call. It is fixed (ADR 20
// binds the credential to api.typesafe.ai); the physical base URL — which a
// test harness may point at a fake server — comes from config via
// JevClient.BaseURL(). The E3 binding checker reads jevHost from the prim's
// Meta to decide whether the credential is bound.
const jevHost = "api.typesafe.ai"

// redactText redacts text with the session's L1 scanner and returns the
// redacted text plus the class names removed.
func redactText(text string, sc *l1.Scanner) (string, []string) {
	res := redact.Redact(text, sc)
	var classes []string
	for _, e := range res.Events {
		classes = append(classes, e.Class)
	}
	return res.Text, classes
}

// jevCall performs one Jev API call: redacts the payload, encodes, sends,
// records the log event, and returns the decoded response as a map.
func jevCall(jc JevClient, c *scheme.Call, tool string, payloadFields map[string]string) (map[string]any, error) {
	// Credential handle is always arg 0.
	if c.Args[0].Kind != scheme.KHandle {
		return nil, fmt.Errorf("%s: arg 1 must be a credential handle", tool)
	}
	cred := c.Args[0].H

	// Redact each payload field.
	body := make(map[string]any, len(payloadFields)+2)
	body["level"] = jc.Level()
	var redacts []string
	for k, v := range payloadFields {
		rt, classes := redactText(v, jc.Redactor())
		body[k] = rt
		redacts = append(redacts, classes...)
	}

	enc, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("%s: encode: %w", tool, err)
	}
	h := sha256.Sum256(enc)
	hash := hex.EncodeToString(h[:8])
	logEv := func(ok bool, e string, score float64) {
		_, _ = jc.Record(nlog.KindJevReq, nlog.JevReq{
			Tool: tool, Level: jc.Level(), Bytes: len(enc),
			PayloadHash: hash, Redactions: redacts, Score: score, OK: ok, Error: e,
		})
	}

	// Build the request.
	base, err := url.Parse(jc.BaseURL())
	if err != nil {
		logEv(false, err.Error(), 0)
		return nil, fmt.Errorf("%s: base url: %w", tool, err)
	}
	u := *base
	u.Path = jevPath

	ctx, cancel := context.WithTimeout(c.Ctx, jc.Timeout())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(enc))
	if err != nil {
		logEv(false, err.Error(), 0)
		return nil, fmt.Errorf("%s: request: %w", tool, err)
	}
	req.Header.Set("Content-Type", "application/json")
	// Inject the Jev credential (bound to api.typesafe.ai).
	if err := jc.InjectHeader(cred.ID, "Authorization", req); err != nil {
		logEv(false, err.Error(), 0)
		return nil, fmt.Errorf("%s: inject credential: %w", tool, err)
	}

	resp, err := jc.Do(req)
	if err != nil {
		logEv(false, err.Error(), 0)
		return nil, fmt.Errorf("%s: %w", tool, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		logEv(false, err.Error(), 0)
		return nil, fmt.Errorf("%s: read body: %w", tool, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		logEv(false, resp.Status, 0)
		return nil, fmt.Errorf("%s: status %s", tool, resp.Status)
	}

	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		logEv(false, "bad json", 0)
		return nil, fmt.Errorf("%s: decode: %w", tool, err)
	}
	if s, ok := out["score"].(float64); ok {
		logEv(true, "", s)
	} else {
		logEv(true, "", 0)
	}
	return out, nil
}

// jevNoul: (handle question) → "plan" | "proceed".
// Asks the Jev gate whether this message needs a plan before proceeding.
func jevNoul(jc JevClient) *scheme.Prim {
	return &scheme.Prim{
		Name: "jev-noul",
		Meta: scheme.Meta{
			Doc:        "Ask the Jev gate whether a message needs a plan: returns \"plan\" or \"proceed\".",
			Egress:     true,
			Host:       true,
			EgressHost: jevHost,
			Params: []scheme.Param{
				{Name: "cred", Type: "any", Role: "credential", Doc: "Jev credential handle"},
				{Name: "question", Type: "string", Role: "payload", Doc: "the user's message text (redacted before send)"},
			},
		},
		Handler: func(c *scheme.Call) (scheme.Value, error) {
			q, err := c.String(1)
			if err != nil {
				return scheme.Void, err
			}
			out, err := jevCall(jc, c, "jev-noul", map[string]string{"question": q})
			if err != nil {
				return scheme.Void, err
			}
			verdict, _ := out["verdict"].(string)
			if verdict == "" {
				return scheme.Void, fmt.Errorf("jev-noul: no verdict in response")
			}
			return scheme.Str(verdict), nil
		},
	}
}

// jevScore: (handle payload ask) → a number in [0,1].
// Scores a plan against the user's ask (Seam A).
func jevScore(jc JevClient) *scheme.Prim {
	return &scheme.Prim{
		Name: "jev-score",
		Meta: scheme.Meta{
			Doc:        "Score a plan against the user's ask: returns a number in [0,1].",
			Egress:     true,
			Host:       true,
			EgressHost: jevHost,
			Params: []scheme.Param{
				{Name: "cred", Type: "any", Role: "credential", Doc: "Jev credential handle"},
				{Name: "payload", Type: "string", Role: "payload", Doc: "the plan text to score (redacted before send)"},
				{Name: "ask", Type: "string", Role: "payload", Doc: "the user's ask text (redacted before send)"},
			},
		},
		Handler: func(c *scheme.Call) (scheme.Value, error) {
			payload, err := c.String(1)
			if err != nil {
				return scheme.Void, err
			}
			ask, err := c.String(2)
			if err != nil {
				return scheme.Void, err
			}
			out, err := jevCall(jc, c, "jev-score", map[string]string{"payload": payload, "ask": ask})
			if err != nil {
				return scheme.Void, err
			}
			score, ok := out["score"].(float64)
			if !ok {
				return scheme.Void, fmt.Errorf("jev-score: no score in response")
			}
			return scheme.Float(score), nil
		},
	}
}

// jevChoice: (handle situation) → a number in [0,1] with a "choice" string.
// Scores an ambiguous egress situation (Seam C: class + destination + scope).
func jevChoice(jc JevClient) *scheme.Prim {
	return &scheme.Prim{
		Name: "jev-choice",
		Meta: scheme.Meta{
			Doc:        "Score an ambiguous egress situation for a recommended choice: returns a score and the chosen option.",
			Egress:     true,
			Host:       true,
			EgressHost: jevHost,
			Params: []scheme.Param{
				{Name: "cred", Type: "any", Role: "credential", Doc: "Jev credential handle"},
				{Name: "situation", Type: "string", Role: "payload", Doc: "the egress situation description (class, dest, scope) (redacted before send)"},
			},
		},
		Handler: func(c *scheme.Call) (scheme.Value, error) {
			situation, err := c.String(1)
			if err != nil {
				return scheme.Void, err
			}
			out, err := jevCall(jc, c, "jev-choice", map[string]string{"situation": situation})
			if err != nil {
				return scheme.Void, err
			}
			score, _ := out["score"].(float64)
			choice, _ := out["choice"].(string)
			return scheme.List(
				scheme.Cons(scheme.Str("score"), scheme.Float(score)),
				scheme.Cons(scheme.Str("choice"), scheme.Str(choice)),
			), nil
		},
	}
}
