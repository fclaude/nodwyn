// Jev egress primitives (ADR 19/20): jev-noul, jev-score, jev-choice.
//
// They are the only Scheme->Jev surface. Each is an Egress primitive with a
// credential slot, so the guard's E3 check (binding table) and the L1 text
// check run before the handler; the handler then redacts every string it is
// about to send with the L1 scanner (ADR 19), POSTs one System One request,
// records a jev.req event (level, bytes, payload hash, redaction classes,
// the answer) -- never the payload -- and returns the answer as a value.
//
// Wire format (docs.typesafe.ai/api): POST /v1/systemone with
// Authorization: Bearer <key> and
//
//	{"state": <text>, "model": "jev-latest", "questions": {"q": {...}}}
//
// where a question is {"type": "noul"|"score"|"choice", "instructions": ...,
// "criteria": ...}; the answer comes back under answers.q.
//
// The handlers do not own the transport, the redactor or the log: the core
// implements JevClient.
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
	"strings"
	"time"

	"github.com/fclaude/nibb/internal/detect/l1"
	nlog "github.com/fclaude/nibb/internal/log"
	"github.com/fclaude/nibb/internal/redact"
	"github.com/fclaude/nibb/internal/scheme"
)

// JevClient is what the Jev primitives need from the core session.
type JevClient interface {
	BaseURL() string // e.g. https://api.typesafe.ai
	Model() string   // e.g. jev-latest
	Timeout() time.Duration
	Level() string // jev.content level, recorded per call
	Redactor() *l1.Scanner
	InjectHeader(id, name, prefix string, req *http.Request) error
	Do(req *http.Request) (*http.Response, error)
	Record(kind nlog.Kind, payload any) (nlog.Event, error)
}

const jevPath = "/v1/systemone"

// jevHost is the destination the E3 binding check judges: the host of the
// configured base URL, so the log never claims one host while bytes go to
// another. Bind class jev-key to it (api.typesafe.ai in production).
func jevHost(jc JevClient) string {
	if u, err := url.Parse(jc.BaseURL()); err == nil && u.Host != "" {
		return strings.ToLower(u.Hostname())
	}
	return strings.ToLower(jc.BaseURL())
}

// question is one System One question.
type question struct {
	Type         string `json:"type"`
	Instructions string `json:"instructions"`
	Criteria     any    `json:"criteria,omitempty"`
}

// jevCall sends one question about state and returns its answer object.
func jevCall(jc JevClient, c *scheme.Call, tool, state string, q question) (map[string]any, error) {
	if c.Args[0].Kind != scheme.KHandle {
		return nil, fmt.Errorf("%s: arg 1 must be a credential handle", tool)
	}
	cred := c.Args[0].H
	sc := jc.Redactor()

	// Redact everything that leaves: the state and the instructions.
	var redactions []string
	rstate, rs := redactText(state, sc)
	redactions = append(redactions, rs...)
	q.Instructions, rs = redactText(q.Instructions, sc)
	redactions = append(redactions, rs...)

	body := map[string]any{
		"state":     rstate,
		"model":     jc.Model(),
		"questions": map[string]question{"q": q},
	}
	enc, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("%s: encode: %w", tool, err)
	}
	sum := sha256.Sum256(enc)
	hash := hex.EncodeToString(sum[:8])
	record := func(ok bool, errText string, answer any) {
		_, _ = jc.Record(nlog.KindJevReq, nlog.JevReq{
			Tool: tool, Level: jc.Level(), Bytes: len(enc), PayloadHash: hash,
			Redactions: redactions, OK: ok, Error: errText, Answer: answer,
		})
	}

	base, err := url.Parse(jc.BaseURL())
	if err != nil {
		record(false, err.Error(), nil)
		return nil, fmt.Errorf("%s: base url: %w", tool, err)
	}
	u := *base
	u.Path = jevPath
	ctx, cancel := context.WithTimeout(c.Ctx, jc.Timeout())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(enc))
	if err != nil {
		record(false, err.Error(), nil)
		return nil, fmt.Errorf("%s: request: %w", tool, err)
	}
	req.Header.Set("Content-Type", "application/json")
	if err := jc.InjectHeader(cred.ID, "Authorization", "Bearer ", req); err != nil {
		record(false, err.Error(), nil)
		return nil, fmt.Errorf("%s: inject credential: %w", tool, err)
	}
	resp, err := jc.Do(req)
	if err != nil {
		record(false, err.Error(), nil)
		return nil, fmt.Errorf("%s: %w", tool, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		record(false, err.Error(), nil)
		return nil, fmt.Errorf("%s: read body: %w", tool, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		record(false, resp.Status, nil)
		return nil, fmt.Errorf("%s: %s", tool, resp.Status)
	}
	var out struct {
		Answers map[string]map[string]any `json:"answers"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		record(false, "bad json", nil)
		return nil, fmt.Errorf("%s: decode: %w", tool, err)
	}
	ans, ok := out.Answers["q"]
	if !ok {
		record(false, "no answer", nil)
		return nil, fmt.Errorf("%s: no answer in response", tool)
	}
	record(true, "", ans)
	return ans, nil
}

func redactText(text string, sc *l1.Scanner) (string, []string) {
	res := redact.Redact(text, sc)
	var classes []string
	for _, e := range res.Events {
		classes = append(classes, e.Class)
	}
	return res.Text, classes
}

func credParam() scheme.Param {
	return scheme.Param{Name: "cred", Type: "any", Role: "credential", Doc: "the jev-key credential handle"}
}

// jevNoul: (jev-noul cred state instructions) -> probability that the
// answer to the yes/no instructions about state is yes.
func jevNoul(jc JevClient) *scheme.Prim {
	return &scheme.Prim{
		Name: "jev-noul",
		Meta: scheme.Meta{
			Doc:         "Ask Jev a yes/no question about a text: returns the probability (0..1) that the answer is yes.",
			Egress:      true,
			Host:        true,
			EgressHost:  jevHost(jc),
			TaintSource: scheme.TagHTTP,
			Params: []scheme.Param{credParam(),
				{Name: "state", Type: "string", Role: "payload", Doc: "the text to judge (redacted before send)"},
				{Name: "instructions", Type: "string", Role: "payload", Doc: "the yes/no question"},
			},
		},
		Handler: func(c *scheme.Call) (scheme.Value, error) {
			state, err := c.String(1)
			if err != nil {
				return scheme.Void, err
			}
			instr, err := c.String(2)
			if err != nil {
				return scheme.Void, err
			}
			ans, err := jevCall(jc, c, "jev-noul", state, question{Type: "noul", Instructions: instr})
			if err != nil {
				return scheme.Void, err
			}
			p, ok := ans["noul"].(float64)
			if !ok {
				return scheme.Void, fmt.Errorf("jev-noul: no noul value in answer")
			}
			return scheme.Float(p), nil
		},
	}
}

// jevScore: (jev-score cred state instructions levels) -> alist with the
// score normalised to 0..1, the raw score, the level count and confidence.
// levels is a list of 2..10 descriptions from low to high.
func jevScore(jc JevClient) *scheme.Prim {
	return &scheme.Prim{
		Name: "jev-score",
		Meta: scheme.Meta{
			Doc:         "Rate a text along an ordered rubric: returns ((\"score\" . 0..1) (\"raw\" . n) (\"levels\" . k) (\"confidence\" . c)).",
			Egress:      true,
			Host:        true,
			EgressHost:  jevHost(jc),
			TaintSource: scheme.TagHTTP,
			Params: []scheme.Param{credParam(),
				{Name: "state", Type: "string", Role: "payload", Doc: "the text to rate (redacted before send)"},
				{Name: "instructions", Type: "string", Role: "payload", Doc: "what to rate it on"},
				{Name: "levels", Type: "list", Doc: "2 to 10 level descriptions, low to high"},
			},
		},
		Handler: func(c *scheme.Call) (scheme.Value, error) {
			state, err := c.String(1)
			if err != nil {
				return scheme.Void, err
			}
			instr, err := c.String(2)
			if err != nil {
				return scheme.Void, err
			}
			items, err := c.List(3)
			if err != nil {
				return scheme.Void, err
			}
			if len(items) < 2 || len(items) > 10 {
				return scheme.Void, fmt.Errorf("jev-score: levels must have 2 to 10 entries, got %d", len(items))
			}
			levels := make([]string, len(items))
			for i, it := range items {
				if it.Kind != scheme.KString {
					return scheme.Void, fmt.Errorf("jev-score: level %d is not a string", i)
				}
				levels[i] = it.S
			}
			ans, err := jevCall(jc, c, "jev-score", state, question{Type: "score", Instructions: instr, Criteria: levels})
			if err != nil {
				return scheme.Void, err
			}
			raw, ok := ans["score"].(float64)
			if !ok {
				return scheme.Void, fmt.Errorf("jev-score: no score in answer")
			}
			conf, _ := ans["confidence"].(float64)
			norm := raw / float64(len(levels)-1)
			return scheme.List(
				scheme.Cons(scheme.Str("score"), scheme.Float(norm)),
				scheme.Cons(scheme.Str("raw"), scheme.Float(raw)),
				scheme.Cons(scheme.Str("levels"), scheme.Int(int64(len(levels)))),
				scheme.Cons(scheme.Str("confidence"), scheme.Float(conf)),
			), nil
		},
	}
}

// jevChoice: (jev-choice cred state instructions options) -> alist with the
// chosen option, confidence and the probability of each option. options is
// an alist of option name -> description (Seam C: class, destination and
// scope go in state; the options are what to do).
func jevChoice(jc JevClient) *scheme.Prim {
	return &scheme.Prim{
		Name: "jev-choice",
		Meta: scheme.Meta{
			Doc:         "Pick one of several options for a text: returns ((\"choice\" . name) (\"confidence\" . c) (\"probabilities\" . ((name . p) ...))).",
			Egress:      true,
			Host:        true,
			EgressHost:  jevHost(jc),
			TaintSource: scheme.TagHTTP,
			Params: []scheme.Param{credParam(),
				{Name: "state", Type: "string", Role: "payload", Doc: "the situation to judge (redacted before send)"},
				{Name: "instructions", Type: "string", Role: "payload", Doc: "the question"},
				{Name: "options", Type: "alist", Doc: "option name -> description"},
			},
		},
		Handler: func(c *scheme.Call) (scheme.Value, error) {
			state, err := c.String(1)
			if err != nil {
				return scheme.Void, err
			}
			instr, err := c.String(2)
			if err != nil {
				return scheme.Void, err
			}
			items, err := c.List(3)
			if err != nil {
				return scheme.Void, err
			}
			opts := map[string]string{}
			for _, it := range items {
				if it.Kind != scheme.KPair || it.P.Car.Kind != scheme.KString || it.P.Cdr.Kind != scheme.KString {
					return scheme.Void, fmt.Errorf("jev-choice: options must be an alist of string -> string")
				}
				opts[it.P.Car.S] = it.P.Cdr.S
			}
			if len(opts) < 2 {
				return scheme.Void, fmt.Errorf("jev-choice: at least two options are required")
			}
			ans, err := jevCall(jc, c, "jev-choice", state, question{Type: "choice", Instructions: instr, Criteria: opts})
			if err != nil {
				return scheme.Void, err
			}
			choice, _ := ans["choice"].(string)
			conf, _ := ans["confidence"].(float64)
			var probs []scheme.Value
			if pm, ok := ans["probabilities"].(map[string]any); ok {
				for k, v := range pm {
					if f, ok := v.(float64); ok {
						probs = append(probs, scheme.Cons(scheme.Str(k), scheme.Float(f)))
					}
				}
			}
			return scheme.List(
				scheme.Cons(scheme.Str("choice"), scheme.Str(choice)),
				scheme.Cons(scheme.Str("confidence"), scheme.Float(conf)),
				scheme.Cons(scheme.Str("probabilities"), scheme.List(probs...)),
			), nil
		},
	}
}
