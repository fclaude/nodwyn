// Package log is nodwyn's append-only session log: the single source of truth
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

	"github.com/fclaude/nodwyn/internal/redact"
)

// Kind is an event kind. The set is fixed (ADR 2); a reader rejects any other.
type Kind string

const (
	KindSessionSchedule  Kind = "session.schedule"
	KindScheduleRun      Kind = "schedule.run"
	KindScheduleSkip     Kind = "schedule.skip"
	KindContextHandover  Kind = "context.handover"
	KindRunState         Kind = "run.state"
	KindRunTurn          Kind = "run.turn"
	KindRunCheckpoint    Kind = "run.checkpoint"
	KindSessionStart     Kind = "session.start"     // session identity: version, cwd, config summary
	KindSessionResume    Kind = "session.resume"    // restarted process, same conversation
	KindSessionModel     Kind = "session.model"     // the model the conversation talks to changed (ADR 40)
	KindSessionJudge     Kind = "session.judge"     // the judge the task asks was chosen (ADR 47)
	KindSessionDelegates Kind = "session.delegates" // the models the task may delegate to were chosen (ADR 27)
	KindSessionLimits    Kind = "session.limits"    // the task's turn limits were turned off or on (ADR 49)
	KindTurnCheckpoint   Kind = "turn.checkpoint"   // budget state and operator answer
	KindTurnEnd          Kind = "turn.end"          // durable end of a submitted turn
	KindSessionEnd       Kind = "session.end"       // clean shutdown; absent after a crash
	KindEvalReq          Kind = "eval.req"          // explicit user Scheme evaluation
	KindEvalResult       Kind = "eval.result"       // evaluation outcome, redacted
	KindUserMsg          Kind = "user.msg"          // text the user sent (redacted from P7)
	KindHarnessMsg       Kind = "harness.msg"       // text the loop's strategy added for the model (add-message)
	KindContextNarrow    Kind = "context.narrow"    // the window starts here, with its own system prompt (narrow-context)
	KindContextWiden     Kind = "context.widen"     // the window is the whole conversation again (widen)
	KindContextFold      Kind = "context.fold"      // the innermost narrowed context ends, hidden from later windows (fold-context)
	KindResultFold       Kind = "result.fold"       // later windows show a tool result as the given text (fold-result)
	KindContextCompact   Kind = "context.compact"   // one compaction: what set it off, whether it compacted, the context's size before and after
	KindLLMReq           Kind = "llm.req"           // request to the control-plane model (window snapshot metadata)
	KindLLMResp          Kind = "llm.resp"          // model output: text, thinking, tool calls, usage
	KindToolCall         Kind = "tool.call"         // a tool application about to be evaluated
	KindToolResult       Kind = "tool.result"       // its result or error (redacted from P7)
	KindBindingChange    Kind = "binding.change"    // requested/applied/failed persistent binding update
	KindSettingChange    Kind = "setting.change"    // a setting in config.toml changed by a person's answer or the console
	KindApprovalChange   Kind = "approval.change"   // explicit temporary policy expansion
	KindGuardDecision    Kind = "guard.decision"    // Allow / Deny / Confirm at apply->Prim
	KindEgressCheck      Kind = "egress.check"      // binding lookup for a secret-bearing egress (P3)
	KindGatePlan         Kind = "gate.plan"         // noul gate + Seam A outcome per message (P5)
	KindPlanStep         Kind = "plan.step"         // the model marked a step of the turn's plan done or skipped
	KindJevReq           Kind = "jev.req"           // a Jev call: content level, bytes, payload hash, redaction, score (P5)
	KindCredRegister     Kind = "cred.register"     // class + handle id only, never a value (P3)
	KindCredRefresh      Kind = "cred.refresh"      // an OAuth2 credential refreshed before use: class, handle id, endpoint, expiry; never a token (ADR 24)
	KindMCPEgress        Kind = "mcp.egress"        // a contained MCP server reaching, or refused, a host through its egress proxy (ADR 23 W3b)
	KindRedact           Kind = "redact.event"      // a [REDACTED class] substitution at an ingress (P7)
	KindSummaryDrop      Kind = "summary.drop"      // revoke a summary without deleting history
	KindSummary          Kind = "summary"           // a rebuildable summary covering a seq range (P10)
	KindNote             Kind = "note"              // free-form operator annotation
	KindFileVersion      Kind = "file.version"      // a file's previous content kept before a write (ADR 22)
	KindAttach           Kind = "attach"            // a file the person attached: its extracted, redacted text (ADR 22)
	KindDocUpload        Kind = "doc.upload"        // a document sent to a document service: file, size, hash, destination, the document's id; never content (W5)
	KindSessionTitle     Kind = "session.title"     // the name a person gave the session; an empty one gives it back its first message (ADR 39)
	KindSessionArchive   Kind = "session.archive"   // a person archived the session or brought it back (ADR 39)
	KindIndexUpdate      Kind = "index.update"      // a build or refresh of the repository's index: what it read, removed, skipped and was refused; never content (ADR 50)
)

// A session's git worktree (ADR 34).
const (
	KindWorktreeCreate  Kind = "worktree.create"  // made for the session: repository, path, branch, base
	KindWorktreeRestore Kind = "worktree.restore" // made again by a resume that found it missing
	KindWorktreeRemove  Kind = "worktree.remove"  // removed on close, with its branch: it held nothing unlanded
)

var kinds = map[Kind]bool{
	KindSessionSchedule: true, KindScheduleRun: true, KindScheduleSkip: true, KindContextHandover: true, KindRunState: true, KindRunTurn: true, KindRunCheckpoint: true,
	KindSessionStart: true, KindSessionEnd: true, KindSessionResume: true, KindSessionModel: true, KindSessionJudge: true, KindSessionDelegates: true, KindSessionLimits: true, KindTurnEnd: true, KindTurnCheckpoint: true,
	KindUserMsg: true, KindHarnessMsg: true, KindContextNarrow: true, KindContextWiden: true, KindContextFold: true, KindResultFold: true, KindContextCompact: true, KindEvalReq: true, KindEvalResult: true, KindLLMReq: true, KindLLMResp: true,
	KindToolCall: true, KindToolResult: true,
	KindGuardDecision: true, KindApprovalChange: true, KindBindingChange: true, KindSettingChange: true, KindSummaryDrop: true, KindEgressCheck: true, KindGatePlan: true, KindPlanStep: true,
	KindJevReq: true, KindCredRegister: true, KindCredRefresh: true, KindMCPEgress: true, KindRedact: true, KindSummary: true, KindNote: true, KindFileVersion: true, KindAttach: true,
	KindDocUpload: true, KindSessionTitle: true, KindSessionArchive: true, KindIndexUpdate: true,
	KindWorktreeCreate: true, KindWorktreeRestore: true, KindWorktreeRemove: true,
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
	// broken is why an earlier event could not be written and the log not
	// put back as it was: every later Append returns it, as one more line
	// would be glued to a partial one or repeat a seq, and Read could no
	// longer read the log. Open and OpenAt, which cut a torn tail, start a
	// Writer that is not broken.
	broken error
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
	return New(lockedFile{f}, session, opts...), nil
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
	w := New(lockedFile{f}, last.Session, opts...)
	w.seq = last.Seq
	if truncated {
		return w, ErrTruncated
	}
	return w, nil
}

// OpenAt is Open for a log whose complete events up to seq end at byte
// offset end, as its index says (ADR 49 §10): only what follows is read, to
// find the last complete event and cut a torn last line, instead of the
// whole log. A log that does not end a line at end is refused, and so is one
// that holds less than the index says.
func OpenAt(path, session string, seq uint64, end int64, opts ...Option) (*Writer, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("log: open: %w", err)
	}
	fail := func(err error) (*Writer, error) { f.Close(); return nil, err }
	if err := lock(f); err != nil {
		return fail(err)
	}
	if seq == 0 || end <= 0 {
		return fail(fmt.Errorf("log: open %s: no complete events", path))
	}
	var newline [1]byte
	if _, err := f.ReadAt(newline[:], end-1); err != nil || newline[0] != '\n' {
		return fail(fmt.Errorf("log: open %s: no event ends at byte %d", path, end))
	}
	if _, err := f.Seek(end, io.SeekStart); err != nil {
		return fail(err)
	}
	last, lastEnd := seq, end
	err = ReplayFrom(f, seq+1, func(ev Event, _ []byte, e int64) error {
		last, lastEnd = ev.Seq, end+e
		return nil
	})
	truncated := errors.Is(err, ErrTruncated)
	if err != nil && !truncated {
		return fail(err)
	}
	if truncated {
		// The lines before the torn one are left as they are: the log is
		// cut at the end of the last complete one.
		if err := f.Truncate(lastEnd); err != nil {
			return fail(err)
		}
	}
	w := New(lockedFile{f}, session, opts...)
	w.seq = last
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
	if w.broken != nil {
		return Event{}, w.broken
	}
	// A file's size before the line is where it is cut back to when the
	// line, or its sync, fails (a full disk): the event is then simply not
	// written, and the next one can be.
	file, _ := w.w.(cuttable)
	size := int64(-1)
	if file != nil {
		if st, err := file.Stat(); err == nil {
			size = st.Size()
		}
	}
	if _, err := w.w.Write(line); err != nil {
		return Event{}, w.undo(file, size, fmt.Errorf("log: write: %w", err))
	}
	if w.syncer != nil {
		if err := w.syncer.Sync(); err != nil {
			return Event{}, w.undo(file, size, fmt.Errorf("log: fsync: %w", err))
		}
	}
	w.seq = ev.Seq
	return ev, nil
}

// cuttable is a log file that can be cut back to an earlier size.
type cuttable interface {
	Stat() (os.FileInfo, error)
	Truncate(int64) error
	Sync() error
}

// undo puts the log back as it was before a line that failed, or, when it
// cannot, marks the Writer broken. It returns err.
func (w *Writer) undo(file cuttable, size int64, err error) error {
	if file != nil && size >= 0 && file.Truncate(size) == nil && file.Sync() == nil {
		return err
	}
	w.broken = fmt.Errorf("%w; the log could not be put back as it was, so nothing more is written to it until it is opened again", err)
	return w.broken
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
	return ReplayFrom(r, 1, func(ev Event, _ []byte, _ int64) error { return fn(ev) })
}

// ReplayFrom is Replay over a log read from inside it: r starts at the line
// of event first, which an index of where lines start says (ADR 49 §10).
// fn is also given each event's line, without its newline, and the offset
// just past it, counted from where r starts.
func ReplayFrom(r io.Reader, first uint64, fn func(ev Event, line []byte, end int64) error) error {
	br := bufio.NewReaderSize(r, 64<<10)
	expect := first
	var offset int64
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
		offset += int64(len(line))
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
		if err := fn(ev, line, offset); err != nil {
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

// Remove deletes a session log no process has open. It takes the writer's
// lock first, so a log another process appends to is refused rather than
// removed from under it.
func Remove(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("log: remove: %w", err)
	}
	defer f.Close()
	if err := lock(f); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("log: remove: %w", err)
	}
	return nil
}

// lock gives one process exclusive append ownership until the file is closed.
// The kernel releases it after a crash; there is no stale lock to remove.
func lock(f *os.File) error {
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fmt.Errorf("log: session is already open or cannot be locked: %w", err)
	}
	return nil
}

// lockedFile is a session log file holding the lock taken by lock.
type lockedFile struct{ *os.File }

// Close releases the lock before closing the file. The lock belongs to the
// open file description, and a child that another goroutine forks while the
// log is open shares that description until its exec closes it, so without
// the unlock a resume in that window found the session still locked.
func (f lockedFile) Close() error {
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return f.File.Close()
}
