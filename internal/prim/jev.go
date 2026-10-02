// Jev egress primitives (ADR 19/20): jev-noul, jev-score, jev-choice, and
// the model's judge-noul, judge-score, judge-choice.
//
// They are the only Scheme->Jev surface. Each is an Egress primitive, so the
// guard's E3 check (binding table) and the L1 text check run before the
// handler; the handler then redacts every string it is about to send with
// the L1 scanner (ADR 19), POSTs one System One request, records a jev.req
// event (origin, level, bytes, payload hash, redaction classes, the answer)
// -- never the payload -- and returns the answer as a value.
//
// The jev-* primitives take the jev-key handle as their first argument: the
// plan gate and user code hold one, and they are hidden, since a handle
// cannot come from a model's JSON. The judge-* tools are the same calls
// without it: Go looks up the stored jev-key itself and the guard judges
// the call as carrying that class (Meta.EgressClass, as for an MCP
// server's key). They are offered only when the session starts with a
// jev-key stored and jev.content = "full", because what the model sends is
// its own choice of text, which is the level at which content leaves
// (ADR 20, amended 2026-09-30).
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

	"github.com/fclaude/nodwyn/internal/budget"
	"github.com/fclaude/nodwyn/internal/detect/l1"
	nlog "github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/redact"
	"github.com/fclaude/nodwyn/internal/scheme"
)

// JevOptions are fixed for the session, separate from the host services.
type JevOptions struct {
	URL     string
	Model   string
	Content string
	Timeout time.Duration
	// Tools registers judge-noul, judge-score and judge-choice for the model.
	Tools bool
	// PriceInput and PriceOutput are the judge's prices, USD per million
	// tokens, to estimate what a request cost; nil when not set.
	PriceInput, PriceOutput *float64
}

// jevUsage reads the API's usage object field by field; nil when it gives
// no counts. The cost is estimated from the configured prices.
func jevUsage(raw json.RawMessage, o JevOptions) *nlog.JevUsage {
	var fields struct {
		Input  json.RawMessage `json:"input_tokens"`
		Output json.RawMessage `json:"output_tokens"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &fields) != nil {
		return nil
	}
	count := func(raw json.RawMessage) (int, bool) {
		var n int
		if len(raw) == 0 || json.Unmarshal(raw, &n) != nil || n < 0 {
			return 0, false
		}
		return n, true
	}
	in, gotIn := count(fields.Input)
	out, gotOut := count(fields.Output)
	if !gotIn && !gotOut {
		return nil
	}
	u := &nlog.JevUsage{InputTokens: in, OutputTokens: out}
	if o.PriceInput != nil && o.PriceOutput != nil {
		c := (float64(in)**o.PriceInput + float64(out)**o.PriceOutput) / 1e6
		u.Cost = &c
	}
	return u
}

// JevClient supplies the shared host services used by Jev primitives.
// Credential values remain behind injection; handlers never open the
// keychain, and HandleForClass returns a handle, never a value.
type JevClient interface {
	Redactor() *l1.Scanner
	HandleForClass(class string) *scheme.Handle
	InjectHeader(id, name, prefix string, req *http.Request) error
	Do(req *http.Request) (*http.Response, error)
	Record(kind nlog.Kind, payload any) (nlog.Event, error)
}

// JevClass is the credential class of the Jev key.
const JevClass = "jev-key"

const (
	jevPath     = "/v1/systemone"
	jevPosition = "header:Authorization:Bearer"
)

// jevHost is the destination the E3 binding check judges: the host of the
// configured base URL, so the log never claims one host while bytes go to
// another. Bind class jev-key to it (api.typesafe.ai in production).
func jevHost(o JevOptions) string {
	if u, err := url.Parse(o.URL); err == nil && u.Host != "" {
		return strings.ToLower(u.Hostname())
	}
	return strings.ToLower(o.URL)
}

// question is one System One question.
type question struct {
	Type         string `json:"type"`
	Instructions string `json:"instructions"`
	Criteria     any    `json:"criteria,omitempty"`
}

// jevCred is the key a call sends: the handle in its first argument for a
// jev-* primitive, the stored jev-key for a judge-* tool.
func jevCred(jc JevClient, c *scheme.Call, keyed bool, tool string) (*scheme.Handle, error) {
	if keyed {
		if c.Args[0].Kind != scheme.KHandle {
			return nil, fmt.Errorf("%s: arg 1 must be a credential handle", tool)
		}
		return c.Args[0].H, nil
	}
	h := jc.HandleForClass(JevClass)
	if h == nil {
		return nil, fmt.Errorf("%s: no %s credential is stored", tool, JevClass)
	}
	return h, nil
}

// jevCall sends one question about state and returns its answer object.
func jevCall(o JevOptions, jc JevClient, c *scheme.Call, keyed bool, tool, state string, q question) (map[string]any, error) {
	cred, err := jevCred(jc, c, keyed, tool)
	if err != nil {
		return nil, err
	}
	sc := jc.Redactor()

	// Redact everything that leaves: the state, the instructions and the
	// criteria (levels, or option names and descriptions).
	var redactions []string
	rstate, rs := redactText(state, sc)
	redactions = append(redactions, rs...)
	q.Instructions, rs = redactText(q.Instructions, sc)
	redactions = append(redactions, rs...)
	switch crit := q.Criteria.(type) {
	case []string:
		out := make([]string, len(crit))
		for i, level := range crit {
			out[i], rs = redactText(level, sc)
			redactions = append(redactions, rs...)
		}
		q.Criteria = out
	case map[string]string:
		out := make(map[string]string, len(crit))
		for name, desc := range crit {
			rname, rn := redactText(name, sc)
			rdesc, rd := redactText(desc, sc)
			redactions = append(append(redactions, rn...), rd...)
			out[rname] = rdesc
		}
		q.Criteria = out
	}

	body := map[string]any{
		"state":     rstate,
		"model":     o.Model,
		"questions": map[string]question{"q": q},
	}
	enc, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("%s: encode: %w", tool, err)
	}
	sum := sha256.Sum256(enc)
	hash := hex.EncodeToString(sum[:8])
	var model string
	var used *nlog.JevUsage
	record := func(ok bool, errText string, answer any) {
		_, _ = jc.Record(nlog.KindJevReq, nlog.JevReq{
			Tool: tool, Origin: string(c.Origin), Level: o.Content, Bytes: len(enc), PayloadHash: hash,
			Redactions: redactions, OK: ok, Error: errText, Answer: answer, Model: model, Usage: used,
		})
	}

	base, err := url.Parse(o.URL)
	if err != nil {
		record(false, err.Error(), nil)
		return nil, fmt.Errorf("%s: base url: %w", tool, err)
	}
	u := *base
	u.Path = jevPath
	ctx, cancel := context.WithTimeout(c.Ctx, o.Timeout)
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
		Model   string                    `json:"model"`
		Answers map[string]map[string]any `json:"answers"`
		Usage   json.RawMessage           `json:"usage"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		record(false, "bad json", nil)
		return nil, fmt.Errorf("%s: decode: %w", tool, err)
	}
	// What it used is counted whatever the answer: the request was made.
	model, used = out.Model, jevUsage(out.Usage, o)
	if t := budget.From(c.Ctx); t != nil && used != nil {
		t.AddJudge(used.InputTokens+used.OutputTokens, used.Cost)
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

// jevMeta is the metadata of one judge primitive. keyed takes the handle
// first and is hidden; otherwise the call carries the stored jev-key.
func jevMeta(o JevOptions, keyed bool, doc string, params ...scheme.Param) scheme.Meta {
	m := scheme.Meta{
		Doc:            doc,
		Egress:         true,
		Host:           true,
		EgressHost:     jevHost(o),
		EgressMethod:   http.MethodPost,
		EgressPosition: jevPosition,
		TaintSource:    scheme.TagHTTP,
		Params:         params,
	}
	if keyed {
		m.Hidden = true
		m.Params = append([]scheme.Param{{Name: "cred", Type: "any", Role: "credential", Doc: "the jev-key credential handle"}}, params...)
	} else {
		m.EgressClass = JevClass
		m.Doc += " The text leaves the machine for an external judge (Jev); known secret shapes are redacted first."
	}
	return m
}

// jevName is jev-KIND for the keyed primitive, judge-KIND for the tool.
func jevName(keyed bool, kind string) string {
	if keyed {
		return "jev-" + kind
	}
	return "judge-" + kind
}

// jevNoul: (jev-noul cred state instructions), or (judge-noul state
// instructions) -> probability that the answer to the yes/no instructions
// about state is yes.
func jevNoul(o JevOptions, jc JevClient, keyed bool) *scheme.Prim {
	name := jevName(keyed, "noul")
	k := 0
	if keyed {
		k = 1
	}
	return &scheme.Prim{
		Name: name,
		Meta: jevMeta(o, keyed, "Ask Jev a yes/no question about a text: returns the probability (0..1) that the answer is yes.",
			scheme.Param{Name: "state", Type: "string", Role: "payload", Doc: "the text to judge (redacted before send)"},
			scheme.Param{Name: "instructions", Type: "string", Role: "payload", Doc: "the yes/no question"},
		),
		Handler: func(c *scheme.Call) (scheme.Value, error) {
			state, err := c.String(k)
			if err != nil {
				return scheme.Void, err
			}
			instr, err := c.String(k + 1)
			if err != nil {
				return scheme.Void, err
			}
			ans, err := jevCall(o, jc, c, keyed, name, state, question{Type: "noul", Instructions: instr})
			if err != nil {
				return scheme.Void, err
			}
			p, ok := ans["noul"].(float64)
			if !ok {
				return scheme.Void, fmt.Errorf("%s: no noul value in answer", name)
			}
			return scheme.Float(p), nil
		},
	}
}

// jevScore: (jev-score cred state instructions levels), or (judge-score
// state instructions levels) -> alist with the score normalised to 0..1,
// the raw score, the level count and confidence. levels is a list of 2..10
// descriptions from low to high.
func jevScore(o JevOptions, jc JevClient, keyed bool) *scheme.Prim {
	name := jevName(keyed, "score")
	k := 0
	if keyed {
		k = 1
	}
	return &scheme.Prim{
		Name: name,
		Meta: jevMeta(o, keyed, "Rate a text along an ordered rubric: returns ((\"score\" . 0..1) (\"raw\" . n) (\"levels\" . k) (\"confidence\" . c)).",
			scheme.Param{Name: "state", Type: "string", Role: "payload", Doc: "the text to rate (redacted before send)"},
			scheme.Param{Name: "instructions", Type: "string", Role: "payload", Doc: "what to rate it on"},
			scheme.Param{Name: "levels", Type: "list", Role: "payload", Doc: "2 to 10 level descriptions, low to high"},
		),
		Handler: func(c *scheme.Call) (scheme.Value, error) {
			state, err := c.String(k)
			if err != nil {
				return scheme.Void, err
			}
			instr, err := c.String(k + 1)
			if err != nil {
				return scheme.Void, err
			}
			items, err := c.List(k + 2)
			if err != nil {
				return scheme.Void, err
			}
			if len(items) < 2 || len(items) > 10 {
				return scheme.Void, fmt.Errorf("%s: levels must have 2 to 10 entries, got %d", name, len(items))
			}
			levels := make([]string, len(items))
			for i, it := range items {
				if it.Kind != scheme.KString {
					return scheme.Void, fmt.Errorf("%s: level %d is not a string", name, i)
				}
				levels[i] = it.S
			}
			ans, err := jevCall(o, jc, c, keyed, name, state, question{Type: "score", Instructions: instr, Criteria: levels})
			if err != nil {
				return scheme.Void, err
			}
			raw, ok := ans["score"].(float64)
			if !ok {
				return scheme.Void, fmt.Errorf("%s: no score in answer", name)
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

// jevChoice: (jev-choice cred state instructions options), or
// (judge-choice state instructions options) -> alist with the chosen
// option, confidence and the probability of each option. options is an
// alist of option name -> description (Seam C: class, destination and
// scope go in state; the options are what to do).
func jevChoice(o JevOptions, jc JevClient, keyed bool) *scheme.Prim {
	name := jevName(keyed, "choice")
	k := 0
	if keyed {
		k = 1
	}
	return &scheme.Prim{
		Name: name,
		Meta: jevMeta(o, keyed, "Pick one of several options for a text: returns ((\"choice\" . name) (\"confidence\" . c) (\"probabilities\" . ((name . p) ...))).",
			scheme.Param{Name: "state", Type: "string", Role: "payload", Doc: "the situation to judge (redacted before send)"},
			scheme.Param{Name: "instructions", Type: "string", Role: "payload", Doc: "the question"},
			scheme.Param{Name: "options", Type: "alist", Role: "payload", Doc: "option name -> description"},
		),
		Handler: func(c *scheme.Call) (scheme.Value, error) {
			state, err := c.String(k)
			if err != nil {
				return scheme.Void, err
			}
			instr, err := c.String(k + 1)
			if err != nil {
				return scheme.Void, err
			}
			items, err := c.List(k + 2)
			if err != nil {
				return scheme.Void, err
			}
			opts := map[string]string{}
			for _, it := range items {
				if it.Kind != scheme.KPair || it.P.Car.Kind != scheme.KString || it.P.Cdr.Kind != scheme.KString {
					return scheme.Void, fmt.Errorf("%s: options must be an alist of string -> string", name)
				}
				opts[it.P.Car.S] = it.P.Cdr.S
			}
			if len(opts) < 2 {
				return scheme.Void, fmt.Errorf("%s: at least two options are required", name)
			}
			ans, err := jevCall(o, jc, c, keyed, name, state, question{Type: "choice", Instructions: instr, Criteria: opts})
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
