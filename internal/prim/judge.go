// The judge's egress primitives (ADR 19/20, ADR 46): judge-yes-no,
// judge-score and judge-choice.
//
// They are the only Scheme->judge surface. Each is an Egress primitive, so
// the guard's E3 check (binding table) and the L1 text check run before the
// handler; the handler then redacts every string it is about to send with
// the L1 scanner (ADR 19), POSTs one request to the judge's server, records
// a jev.req event (origin, level, bytes, payload hash, redaction classes,
// the answer) -- never the payload -- and returns the answer as a value.
//
// The judge is a provider's (ADR 46): its server's provider says which API
// the request speaks, its address where it goes, and its key what it
// carries. Go looks the stored key up itself at call time, and the guard
// judges every call as carrying its class (Meta.EgressClass, as for an MCP
// server's key), so no Scheme holds a handle to ask the judge. The core
// registers them only when the session starts with the key stored. They are
// hidden, for the plan gate and the user's Scheme, unless judge.content is
// "full": then they are the model's tools too, because what the model sends
// is its own choice of text, which is the level at which content leaves
// (ADR 20, amended 2026-09-30). A tool the user registers may call them,
// with the model's origin, at any level, as it could with a handle its
// init.scm looked up before: which judge-backed tools exist is the user's
// to decide (ADR 20 §3).
//
// Wire formats, each a POST with Authorization: Bearer <key>:
//
//   - Jev (provider "jev", docs.typesafe.ai/api): /v1/systemone with
//     {"state": <text>, "model": "jev-latest", "questions": {"q": {...}}},
//     where a question is {"type": "noul"|"score"|"choice", "instructions":
//     ..., "criteria": ...}; the answer comes back under answers.q.
//   - OpenAI's Decisions API (provider "openai",
//     developers.openai.com/api/docs/guides/decisions): /decisions after the
//     server's /v1, with {"model": "gpt-6-luna", "input": <text>,
//     "questions": [{"type": "predicate"|"score"|"choice", "name": "q",
//     "instructions": ..., "levels"|"choices": [...]}]}; the answer comes back
//     in answers[], named q, and is read into System One's shape, which the
//     handlers and jev.req keep.
//
// The handlers do not own the transport, the redactor or the log: the core
// implements JudgeClient.
package prim

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/fclaude/brote/scheme"
	"github.com/fclaude/nodwyn/internal/budget"
	"github.com/fclaude/nodwyn/internal/detect/l1"
	nlog "github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/redact"
)

// JudgeOptions are the judge a task asks, separate from the host services.
// A task may choose another between turns (ADR 47), so the handlers ask the
// JudgeClient for them on every call.
type JudgeOptions struct {
	// Provider is the provider of the judge's server, which says the API
	// its requests speak: "jev" for System One, "openai" for the Decisions
	// API.
	Provider string
	// URL is the server's base URL; a request goes to its path followed by
	// the API's own (judgePaths).
	URL   string
	Model string
	// Class is the credential class of the server's key, which every call
	// carries.
	Class   string
	Content string
	Timeout time.Duration
	// Tools offers judge-yes-no, judge-score and judge-choice to the model;
	// otherwise they are hidden, for Scheme alone.
	Tools bool
	// PriceInput and PriceOutput are the judge's prices, USD per million
	// tokens, to estimate what a request cost; nil when not set.
	PriceInput, PriceOutput *float64
}

// judgePaths are where each provider's typed-question API answers, after
// its server's base URL: Jev's address carries no version, while OpenAI's
// carries /v1, as its chat models' does.
var judgePaths = map[string]string{"jev": "/v1/systemone", "openai": "/decisions"}

// judgeUsage reads the API's usage object field by field; nil when it gives
// no counts. The cost is estimated from the configured prices.
func judgeUsage(raw json.RawMessage, o JudgeOptions) *nlog.JevUsage {
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

// JudgeClient supplies the shared host services used by the judge's
// primitives. Credential values remain behind injection; handlers never
// open the keychain, and HandleForClass returns a handle, never a value.
type JudgeClient interface {
	Redactor() *l1.Scanner
	HandleForClass(class string) *scheme.Handle
	InjectHeader(id, name, prefix string, req *http.Request) error
	Do(req *http.Request) (*http.Response, error)
	Record(kind nlog.Kind, payload any) (nlog.Event, error)
	// JudgeOptions are the task's judge now; false when it has none.
	JudgeOptions() (JudgeOptions, bool)
}

const (
	// JudgePosition is where the judge's key goes in a request: a bearer
	// token in the Authorization header, for Jev and OpenAI alike, so its
	// server's binding row allows exactly what the judge's calls carry.
	JudgePosition = "header:Authorization:Bearer"
	// JudgeMethod is the HTTP method of every judge call.
	JudgeMethod = "POST"
)

// JudgeHost is the destination the E3 binding check judges: the host of the
// judge's server's base URL, so the log never claims one host while bytes
// go to another.
func JudgeHost(o JudgeOptions) string {
	if u, err := url.Parse(o.URL); err == nil && u.Host != "" {
		return strings.ToLower(u.Hostname())
	}
	return strings.ToLower(o.URL)
}

// question is one question, as System One takes it.
type question struct {
	Type         string `json:"type"`
	Instructions string `json:"instructions"`
	Criteria     any    `json:"criteria,omitempty"`
}

// judgeCred is the key a call sends: the stored key of the judge's server.
func judgeCred(o JudgeOptions, jc JudgeClient, tool string) (*scheme.Handle, error) {
	h := jc.HandleForClass(o.Class)
	if h == nil {
		return nil, fmt.Errorf("%s: no %s credential is stored", tool, o.Class)
	}
	return h, nil
}

// decisionQuestion is one question as OpenAI's Decisions API takes it.
type decisionQuestion struct {
	Type         string           `json:"type"`
	Name         string           `json:"name"`
	Instructions string           `json:"instructions"`
	Choices      []decisionChoice `json:"choices,omitempty"`
	Levels       []decisionLevel  `json:"levels,omitempty"`
}

type decisionChoice struct {
	Value       string `json:"value"`
	Description string `json:"description"`
}

type decisionLevel struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

// requestBody is the body of a request asking q about state, in the API of
// the judge's provider; q is already redacted.
func requestBody(o JudgeOptions, state string, q question) (any, error) {
	switch o.Provider {
	case "jev":
		return map[string]any{"state": state, "model": o.Model, "questions": map[string]question{"q": q}}, nil
	case "openai":
		dq := decisionQuestion{Type: q.Type, Name: "q", Instructions: q.Instructions}
		switch crit := q.Criteria.(type) {
		case []string:
			// A level's text is its description; its label is its place,
			// 1 the lowest, as the levels are ordered.
			for i, level := range crit {
				dq.Levels = append(dq.Levels, decisionLevel{Label: strconv.Itoa(i + 1), Description: level})
			}
		case map[string]string:
			// System One's options are a map, which JSON sends in key order;
			// the list is sent in the same order.
			for _, name := range slices.Sorted(maps.Keys(crit)) {
				dq.Choices = append(dq.Choices, decisionChoice{Value: name, Description: crit[name]})
			}
		}
		if dq.Type == "noul" {
			dq.Type = "predicate"
		}
		return map[string]any{"model": o.Model, "input": state, "questions": []decisionQuestion{dq}}, nil
	}
	return nil, fmt.Errorf("the judge's server is a %q server; the judge asks Jev or OpenAI", o.Provider)
}

// errRefused is a Decisions answer of type refusal: the judge declined the
// question, which its caller takes as no answer.
var errRefused = errors.New("the judge declined to answer (refusal)")

// readAnswer reads a response body in the API of the judge's provider: the
// model that answered, what it used, and the answer to question q in System
// One's shape ({"noul": p}, {"score": s, "confidence": c}, or {"choice":
// name, "confidence": c, "probabilities": {name: p}}), which is what the
// handlers read and jev.req records.
func readAnswer(o JudgeOptions, raw []byte) (model string, usage json.RawMessage, ans map[string]any, err error) {
	if o.Provider == "jev" {
		var out struct {
			Model   string                    `json:"model"`
			Answers map[string]map[string]any `json:"answers"`
			Usage   json.RawMessage           `json:"usage"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			return "", nil, nil, fmt.Errorf("decode: %w", err)
		}
		return out.Model, out.Usage, out.Answers["q"], nil
	}
	var out struct {
		Model   string `json:"model"`
		Answers []struct {
			Type          string          `json:"type"`
			Name          string          `json:"name"`
			Probability   *float64        `json:"probability"`
			Choice        string          `json:"choice"`
			Score         *float64        `json:"score"`
			Confidence    *float64        `json:"confidence"`
			Probabilities json.RawMessage `json:"probabilities"`
		} `json:"answers"`
		Usage json.RawMessage `json:"usage"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", nil, nil, fmt.Errorf("decode: %w", err)
	}
	for _, a := range out.Answers {
		if a.Name != "q" {
			continue
		}
		ans = map[string]any{}
		if a.Confidence != nil {
			ans["confidence"] = *a.Confidence
		}
		switch a.Type {
		case "refusal":
			return out.Model, out.Usage, nil, errRefused
		case "predicate":
			ans["type"] = "noul"
			if a.Probability != nil {
				ans["noul"] = *a.Probability
			}
		case "score":
			ans["type"] = "score"
			if a.Score != nil {
				ans["score"] = *a.Score
			}
		case "choice":
			ans["type"], ans["choice"] = "choice", a.Choice
			var probs []struct {
				Value       string  `json:"value"`
				Probability float64 `json:"probability"`
			}
			if json.Unmarshal(a.Probabilities, &probs) == nil && len(probs) > 0 {
				byName := map[string]any{}
				for _, p := range probs {
					byName[p.Value] = p.Probability
				}
				ans["probabilities"] = byName
			}
		}
		return out.Model, out.Usage, ans, nil
	}
	return out.Model, out.Usage, nil, nil
}

// judgeAsk sends one question about state and returns its answer object, in
// System One's shape whichever API the judge's server speaks.
func judgeAsk(jc JudgeClient, c *scheme.Call, tool, state string, q question) (map[string]any, error) {
	o, ok := jc.JudgeOptions()
	if !ok {
		return nil, fmt.Errorf("%s: this task has no judge", tool)
	}
	cred, err := judgeCred(o, jc, tool)
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

	body, err := requestBody(o, rstate, q)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", tool, err)
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
	u.Path = strings.TrimRight(base.Path, "/") + judgePaths[o.Provider]
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
	answeredBy, usage, ans, err := readAnswer(o, raw)
	// What it used is counted whatever the answer: the request was made.
	model, used = answeredBy, judgeUsage(usage, o)
	if t := budget.From(c.Ctx); t != nil && used != nil {
		t.AddJudge(used.InputTokens+used.OutputTokens, used.Cost)
	}
	if err != nil {
		record(false, err.Error(), nil)
		return nil, fmt.Errorf("%s: %w", tool, err)
	}
	if ans == nil {
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

// judgeMeta is the metadata of one judge primitive: the call carries the
// stored key of the judge's server, and it is the model's tool only when
// the session offers the judge's tools.
func judgeMeta(o JudgeOptions, doc string, params ...scheme.Param) scheme.Meta {
	return scheme.Meta{
		Doc:            doc + " The text leaves the machine for an external judge; known secret shapes are redacted first.",
		Egress:         true,
		Host:           true,
		Hidden:         !o.Tools,
		EgressHost:     JudgeHost(o),
		EgressMethod:   http.MethodPost,
		EgressPosition: JudgePosition,
		EgressClass:    o.Class,
		TaintSource:    scheme.TagHTTP,
		Params:         params,
	}
}

// RetargetJudge points the judge's primitives at the judge o: the host and
// the key class the guard checks each call against are the new judge's, so
// it never judges one destination while bytes go to another. The tool list
// is fixed for the session (ADR 9), so the primitives are kept and their
// metadata moved. The caller holds the session idle: no call is being
// checked while it changes.
func RetargetJudge(in *scheme.Interp, o JudgeOptions) {
	for _, name := range []string{"judge-yes-no", "judge-score", "judge-choice"} {
		if p, ok := in.Lookup(name); ok {
			p.Meta.EgressHost, p.Meta.EgressClass = JudgeHost(o), o.Class
		}
	}
}

// judgeAvailable: (judge-available?) -> whether the task has a judge now
// whose key is stored, which the plan gate asks before scoring a plan.
func judgeAvailable(jc JudgeClient) *scheme.Prim {
	return &scheme.Prim{
		Name: "judge-available?",
		Meta: scheme.Meta{Doc: "Whether this task has a judge now whose key is stored.", Host: true, Hidden: true},
		Handler: func(*scheme.Call) (scheme.Value, error) {
			o, ok := jc.JudgeOptions()
			return scheme.Bool(ok && jc.HandleForClass(o.Class) != nil), nil
		},
	}
}

// judgeYesNo: (judge-yes-no state instructions) -> probability that the
// answer to the yes/no instructions about state is yes: System One's noul,
// the Decisions API's predicate.
func judgeYesNo(o JudgeOptions, jc JudgeClient) *scheme.Prim {
	const name = "judge-yes-no"
	return &scheme.Prim{
		Name: name,
		Meta: judgeMeta(o, "Ask the judge a yes/no question about a text: returns the probability (0..1) that the answer is yes.",
			scheme.Param{Name: "state", Type: "string", Role: "payload", Doc: "the text to judge (redacted before send)"},
			scheme.Param{Name: "instructions", Type: "string", Role: "payload", Doc: "the yes/no question"},
		),
		Handler: func(c *scheme.Call) (scheme.Value, error) {
			state, err := c.String(0)
			if err != nil {
				return scheme.Void, err
			}
			instr, err := c.String(1)
			if err != nil {
				return scheme.Void, err
			}
			ans, err := judgeAsk(jc, c, name, state, question{Type: "noul", Instructions: instr})
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

// judgeScore: (judge-score state instructions levels) -> alist with the
// score normalised to 0..1, the raw score, the level count and confidence.
// levels is a list of 2..10 descriptions from low to high.
func judgeScore(o JudgeOptions, jc JudgeClient) *scheme.Prim {
	const name = "judge-score"
	return &scheme.Prim{
		Name: name,
		Meta: judgeMeta(o, "Rate a text along an ordered rubric: returns ((\"score\" . 0..1) (\"raw\" . n) (\"levels\" . k) (\"confidence\" . c)).",
			scheme.Param{Name: "state", Type: "string", Role: "payload", Doc: "the text to rate (redacted before send)"},
			scheme.Param{Name: "instructions", Type: "string", Role: "payload", Doc: "what to rate it on"},
			scheme.Param{Name: "levels", Type: "list", Role: "payload", Doc: "2 to 10 level descriptions, low to high"},
		),
		Handler: func(c *scheme.Call) (scheme.Value, error) {
			state, err := c.String(0)
			if err != nil {
				return scheme.Void, err
			}
			instr, err := c.String(1)
			if err != nil {
				return scheme.Void, err
			}
			items, err := c.List(2)
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
			ans, err := judgeAsk(jc, c, name, state, question{Type: "score", Instructions: instr, Criteria: levels})
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

// judgeChoice: (judge-choice state instructions options) -> alist with the
// chosen option, confidence and the probability of each option. options is
// an alist of option name -> description (Seam C: class, destination and
// scope go in state; the options are what to do).
func judgeChoice(o JudgeOptions, jc JudgeClient) *scheme.Prim {
	const name = "judge-choice"
	return &scheme.Prim{
		Name: name,
		Meta: judgeMeta(o, "Pick one of several options for a text: returns ((\"choice\" . name) (\"confidence\" . c) (\"probabilities\" . ((name . p) ...))).",
			scheme.Param{Name: "state", Type: "string", Role: "payload", Doc: "the situation to judge (redacted before send)"},
			scheme.Param{Name: "instructions", Type: "string", Role: "payload", Doc: "the question"},
			scheme.Param{Name: "options", Type: "alist", Role: "payload", Doc: "option name -> description"},
		),
		Handler: func(c *scheme.Call) (scheme.Value, error) {
			state, err := c.String(0)
			if err != nil {
				return scheme.Void, err
			}
			instr, err := c.String(1)
			if err != nil {
				return scheme.Void, err
			}
			items, err := c.List(2)
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
			ans, err := judgeAsk(jc, c, name, state, question{Type: "choice", Instructions: instr, Criteria: opts})
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
