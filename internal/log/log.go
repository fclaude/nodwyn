// Package log is nibb's append-only session log: the single source of truth
// (SPEC invariant 1, ADR 2).
//
// One JSON object per line, one file per session. Everything else the harness
// keeps — the context window, summaries, the structural index — is a derived,
// revocable view that can be recomputed from this file.
//
// The writer fsyncs after every event. Mandatory redaction runs after the
// optional payload Filter, before either persistence or returning the event.
// Core derives its context only from these written events.
package log

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/fclaude/nibb/internal/redact"
)

// Kind is an event kind. The set is fixed (ADR 2); a reader rejects any other.
type Kind string

const (
	KindRunState       Kind = "run.state"
	KindRunTurn        Kind = "run.turn"
	KindRunCheckpoint  Kind = "run.checkpoint"
	KindSessionStart   Kind = "session.start"   // session identity: version, cwd, config summary
	KindSessionResume  Kind = "session.resume"  // restarted process, same conversation
	KindTurnCheckpoint Kind = "turn.checkpoint" // budget state and operator answer
	KindTurnEnd        Kind = "turn.end"        // durable end of a submitted turn
	KindSessionEnd     Kind = "session.end"     // clean shutdown; absent after a crash
	KindEvalReq        Kind = "eval.req"        // explicit user Scheme evaluation
	KindEvalResult     Kind = "eval.result"     // evaluation outcome, redacted
	KindUserMsg        Kind = "user.msg"        // text the user sent (redacted from P7)
	KindHarnessMsg     Kind = "harness.msg"     // text the loop's strategy added for the model (add-message)
	KindLLMReq         Kind = "llm.req"         // request to the control-plane model (window snapshot metadata)
	KindLLMResp        Kind = "llm.resp"        // model output: text, thinking, tool calls, usage
	KindToolCall       Kind = "tool.call"       // a tool application about to be evaluated
	KindToolResult     Kind = "tool.result"     // its result or error (redacted from P7)
	KindBindingChange  Kind = "binding.change"  // requested/applied/failed persistent binding update
	KindApprovalChange Kind = "approval.change" // explicit temporary policy expansion
	KindGuardDecision  Kind = "guard.decision"  // Allow / Deny / Confirm at apply->Prim
	KindEgressCheck    Kind = "egress.check"    // binding lookup for a secret-bearing egress (P3)
	KindGatePlan       Kind = "gate.plan"       // noul gate + Seam A outcome per message (P5)
	KindJevReq         Kind = "jev.req"         // a Jev call: content level, bytes, payload hash, redaction, score (P5)
	KindCredRegister   Kind = "cred.register"   // class + handle id only, never a value (P3)
	KindRedact         Kind = "redact.event"    // a [REDACTED class] substitution at an ingress (P7)
	KindSummaryDrop    Kind = "summary.drop"    // revoke a summary without deleting history
	KindSummary        Kind = "summary"         // a rebuildable summary covering a seq range (P10)
	KindNote           Kind = "note"            // free-form operator annotation
)

var kinds = map[Kind]bool{
	KindRunState: true, KindRunTurn: true, KindRunCheckpoint: true,
	KindSessionStart: true, KindSessionEnd: true, KindSessionResume: true, KindTurnEnd: true, KindTurnCheckpoint: true,
	KindUserMsg: true, KindHarnessMsg: true, KindEvalReq: true, KindEvalResult: true, KindLLMReq: true, KindLLMResp: true,
	KindToolCall: true, KindToolResult: true,
	KindGuardDecision: true, KindApprovalChange: true, KindBindingChange: true, KindSummaryDrop: true, KindEgressCheck: true, KindGatePlan: true,
	KindJevReq: true, KindCredRegister: true, KindRedact: true, KindSummary: true, KindNote: true,
}

// Known reports whether k is one of the fixed event kinds.
func Known(k Kind) bool { return kinds[k] }

// Event is one line of the log.
type Event struct {
	Seq     uint64          `json:"seq"`
	TS      time.Time       `json:"ts"`
	Session string          `json:"session"`
	Kind    Kind            `json:"kind"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// Decode unmarshals the payload into v.
func (e Event) Decode(v any) error {
	if len(e.Payload) == 0 {
		return errors.New("log: event has no payload")
	}
	return json.Unmarshal(e.Payload, v)
}

// Filter transforms a marshalled payload before it is written. The ingress
// redactor installs one in Phase 7. It must return valid JSON.
type Filter func(kind Kind, payload []byte) ([]byte, error)

// Writer appends events to a session log.
type Writer struct {
	mu      sync.Mutex
	w       io.Writer
	syncer  interface{ Sync() error }
	closer  io.Closer
	session string
	seq     uint64
	filter  Filter
	now     func() time.Time
}

// Option configures a Writer.
type Option func(*Writer)

// WithFilter installs a payload filter (see Filter).
func WithFilter(f Filter) Option { return func(w *Writer) { w.filter = f } }

// WithClock replaces the timestamp source (tests).
func WithClock(now func() time.Time) Option { return func(w *Writer) { w.now = now } }

// New wraps an arbitrary io.Writer. If w implements Sync it is called after
// every event; if it implements io.Closer, Close closes it.
func New(w io.Writer, session string, opts ...Option) *Writer {
	lw := &Writer{w: w, session: session, now: time.Now}
	if s, ok := w.(interface{ Sync() error }); ok {
		lw.syncer = s
	}
	if c, ok := w.(io.Closer); ok {
		lw.closer = c
	}
	for _, o := range opts {
		o(lw)
	}
	return lw
}

// Create makes a new session log at path. It refuses to overwrite: a session
// log is created exactly once.
func Create(path, session string, opts ...Option) (*Writer, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("log: create dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("log: create: %w", err)
	}
	if err := lock(f); err != nil {
		f.Close()
		return nil, err
	}
	return New(f, session, opts...), nil
}

// Open resumes appending to an existing session log. The last complete event
// determines the next seq; a truncated final line (a crash between write and
// fsync) is discarded and reported through ErrTruncated so the caller can
// decide whether to log a note about it.
func Open(path string, opts ...Option) (*Writer, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("log: open: %w", err)
	}
	fail := func(err error) (*Writer, error) { f.Close(); return nil, err }
	if err := lock(f); err != nil {
		return fail(err)
	}
	events, err := Read(f)
	truncated := errors.Is(err, ErrTruncated)
	if err != nil && !truncated {
		return fail(err)
	}
	if len(events) == 0 {
		return fail(fmt.Errorf("log: open %s: no complete events", path))
	}
	if truncated {
		if err := truncateToComplete(path, events); err != nil {
			return fail(err)
		}
	}
	last := events[len(events)-1]
	w := New(f, last.Session, opts...)
	w.seq = last.Seq
	if truncated {
		return w, ErrTruncated
	}
	return w, nil
}

// Session returns the session id this writer appends to.
func (w *Writer) Session() string { return w.session }

// Seq returns the seq of the last event written (0 before the first).
func (w *Writer) Seq() uint64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.seq
}

// Append marshals payload, passes it through the filter, writes one line and
// fsyncs. It returns the event as written.
func (w *Writer) Append(kind Kind, payload any) (Event, error) {
	if !Known(kind) {
		return Event{}, fmt.Errorf("log: unknown event kind %q", kind)
	}
	var raw json.RawMessage
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return Event{}, fmt.Errorf("log: marshal %s payload: %w", kind, err)
		}
		raw = b
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.filter != nil && raw != nil {
		b, err := w.filter(kind, raw)
		if err != nil {
			return Event{}, fmt.Errorf("log: filter %s payload: %w", kind, err)
		}
		if !json.Valid(b) {
			return Event{}, fmt.Errorf("log: filter returned invalid JSON for %s", kind)
		}
		raw = b
	}
	// Mandatory final boundary, including writers opened outside core.
	if raw != nil {
		b, _, err := redact.JSON(raw, nil)
		if err != nil {
			return Event{}, fmt.Errorf("log: redaction: %w", err)
		}
		raw = b
	}
	ev := Event{
		Seq:     w.seq + 1,
		TS:      w.now().UTC(),
		Session: w.session,
		Kind:    kind,
		Payload: raw,
	}
	line, err := json.Marshal(ev)
	if err != nil {
		return Event{}, fmt.Errorf("log: marshal event: %w", err)
	}
	line = append(line, '\n')
	if _, err := w.w.Write(line); err != nil {
		return Event{}, fmt.Errorf("log: write: %w", err)
	}
	if w.syncer != nil {
		if err := w.syncer.Sync(); err != nil {
			return Event{}, fmt.Errorf("log: fsync: %w", err)
		}
	}
	w.seq = ev.Seq
	return ev, nil
}

// Close closes the underlying file, if any.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closer != nil {
		return w.closer.Close()
	}
	return nil
}

// ErrTruncated reports that the final line of a log was incomplete. The
// events before it are still returned.
var ErrTruncated = errors.New("log: truncated final line")

// ErrSeqGap reports a break in the seq sequence.
var ErrSeqGap = errors.New("log: seq gap")

// Replay streams events from r to fn in order, validating seq continuity and
// kinds. It stops at the first error from fn. A partial final line yields
// ErrTruncated after every complete event has been delivered.
func Replay(r io.Reader, fn func(Event) error) error {
	br := bufio.NewReaderSize(r, 64<<10)
	var expect uint64 = 1
	for {
		line, err := br.ReadBytes('\n')
		if err != nil && err != io.EOF {
			return fmt.Errorf("log: read: %w", err)
		}
		if err == io.EOF {
			if len(bytes.TrimSpace(line)) > 0 {
				return ErrTruncated
			}
			return nil
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var ev Event
		if err := json.Unmarshal(line, &ev); err != nil {
			return fmt.Errorf("log: seq %d: bad line: %w", expect, err)
		}
		if ev.Seq != expect {
			return fmt.Errorf("%w: want %d, got %d", ErrSeqGap, expect, ev.Seq)
		}
		if !Known(ev.Kind) {
			return fmt.Errorf("log: seq %d: unknown kind %q", ev.Seq, ev.Kind)
		}
		if err := fn(ev); err != nil {
			return err
		}
		expect++
	}
}

// Read returns every complete event in r. See Replay for error semantics.
func Read(r io.Reader) ([]Event, error) {
	var out []Event
	err := Replay(r, func(e Event) error { out = append(out, e); return nil })
	return out, err
}

// ReadFile reads a session log from disk.
func ReadFile(path string) ([]Event, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("log: %w", err)
	}
	defer f.Close()
	return Read(f)
}

// truncateToComplete rewrites path so it ends after the last complete event.
func truncateToComplete(path string, events []Event) error {
	var buf bytes.Buffer
	for _, ev := range events {
		b, err := json.Marshal(ev)
		if err != nil {
			return err
		}
		buf.Write(b)
		buf.WriteByte('\n')
	}
	// Byte-identical re-encoding is not guaranteed across versions, so verify
	// the prefix matches before touching the file; otherwise leave it alone
	// and let the caller see the raw truncated file.
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !bytes.HasPrefix(raw, buf.Bytes()) {
		return fmt.Errorf("log: cannot safely truncate %s: re-encoded prefix differs", path)
	}
	return os.Truncate(path, int64(buf.Len()))
}

// NewSessionID returns a sortable, unique session id: UTC timestamp plus
// 4 random bytes, e.g. 20260920T163800Z-9f3a1c02.
func NewSessionID(now time.Time) string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return now.UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(b[:])
}

// SessionPath returns the log path for a session under dir.
func SessionPath(dir, session string) string {
	return filepath.Join(dir, session+".jsonl")
}

// lock gives one process exclusive append ownership until the file is closed.
// The kernel releases it after a crash; there is no stale lock to remove.
func lock(f *os.File) error {
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fmt.Errorf("log: session is already open or cannot be locked: %w", err)
	}
	return nil
}
