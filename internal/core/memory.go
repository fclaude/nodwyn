package core

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"

	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/memory"
	"github.com/fclaude/nodwyn/internal/redact"
	"github.com/fclaude/nodwyn/internal/retrieval"
)

// A session's memory (ADR 51) is kept beside its log as <id>.mem: what is
// made in Go from the log up to the end of its last whole turn, namely the
// cards of its turns and its compaction notes (memory.Folder), and the
// words of the events a model could have seen (convFold), which
// index-search ranks and recall reads (ADR 50). It keeps where that turn's
// line ends and both folds' state there, so a later read folds in only what
// the log gained. Like the log's index (log_index.go) it is derived and
// revocable: checked against the log's line where it ends before use, made
// again from the log on any mismatch, and deleting it is always safe. Its
// events are redacted again as they are folded. It is written by whichever
// nodwyn process reads it, in nodwyn's data directory, where the model's
// file tools cannot reach (the default denylist).
//
// The file is a line of JSON, memFile, then the words: each document's seq
// and what it is (a convDoc as JSON), then the word index
// (retrieval.Index.AppendBinary). A search reads only its own words'
// postings, and nothing is held between searches.

const memVersion = 2

type memFile struct {
	Version int `json:"version"`
	// The last event folded, at the end of a whole turn.
	Seq   uint64 `json:"seq"`
	Start int64  `json:"start"`
	End   int64  `json:"end"`
	Hash  string `json:"hash"`
	// The folds' states there.
	State memory.State `json:"state"`
	Conv  convFold     `json:"conv"`
	// Entries are the cards and episodes, Docs the documents of the words
	// after this line.
	Entries []memory.Entry `json:"entries"`
	Docs    int            `json:"docs"`
}

func memPath(dir, id string) string { return filepath.Join(dir, id+".mem") }

// logMemory is a log's memory as a search or a recall reads it: its cards
// and episodes, and its events as words, those the .mem keeps and those
// after them, folded at the read.
type logMemory struct {
	id      string
	entries []memory.Entry
	parts   []*wordsPart
	// open is the message that began a turn still open, 0 for none, and
	// asked what it asked.
	open  uint64
	asked string
	last  uint64 // the log's last seq
}

// wordsPart is documents, in seq order, and their words.
type wordsPart struct {
	index *retrieval.Index
	seqs  []uint64
	raw   [][]byte // each document as JSON, without its seq
}

func (p *wordsPart) add(d convDoc, counts map[string]int) {
	raw, _ := json.Marshal(d)
	p.index.AddCounts(counts)
	p.seqs = append(p.seqs, d.Seq)
	p.raw = append(p.raw, raw)
}

// doc is document i.
func (p *wordsPart) doc(i int) convDoc {
	var d convDoc
	_ = json.Unmarshal(p.raw[i], &d)
	d.Seq = p.seqs[i]
	return d
}

// find is the document of the event at seq, -1 for none.
func (p *wordsPart) find(seq uint64) int {
	if i, ok := slices.BinarySearch(p.seqs, seq); ok {
		return i
	}
	return -1
}

func newPart() *wordsPart { return &wordsPart{index: &retrieval.Index{}} }

// appendWords appends a part as a .mem keeps it after its line.
func appendWords(b []byte, p *wordsPart) []byte {
	b = binary.AppendUvarint(b, uint64(len(p.seqs)))
	prev := uint64(0)
	for i, seq := range p.seqs {
		b = binary.AppendUvarint(b, seq-prev)
		b = binary.AppendUvarint(b, uint64(len(p.raw[i])))
		b = append(b, p.raw[i]...)
		prev = seq
	}
	return p.index.AppendBinary(b)
}

var errMalformedMem = errors.New("core: a malformed memory file")

// readWords reads what appendWords wrote, with the postings of the words
// keep allows (all for nil). It also returns the index's bytes, to read
// whole if the part is to be added to.
func readWords(data []byte, keep func(string) bool) (*wordsPart, []byte, error) {
	next := func() (uint64, error) {
		v, n := binary.Uvarint(data)
		if n <= 0 {
			return 0, errMalformedMem
		}
		data = data[n:]
		return v, nil
	}
	n, err := next()
	if err != nil || n > uint64(len(data)) {
		return nil, nil, errMalformedMem
	}
	p := &wordsPart{seqs: make([]uint64, 0, n), raw: make([][]byte, 0, n)}
	seq := uint64(0)
	for range n {
		delta, err := next()
		if err != nil {
			return nil, nil, err
		}
		size, err := next()
		if err != nil || size > uint64(len(data)) || delta == 0 {
			return nil, nil, errMalformedMem
		}
		seq += delta
		p.seqs = append(p.seqs, seq)
		p.raw = append(p.raw, data[:size])
		data = data[size:]
	}
	if p.index, err = retrieval.Decode(data, keep); err != nil {
		return nil, nil, err
	}
	if p.index.Len() != len(p.seqs) {
		return nil, nil, errMalformedMem
	}
	return p, data, nil
}

// memFold folds a log's events, in order, into its memory: documents are
// kept once no turn is open after them, and those after the last such
// point are the rest.
type memFold struct {
	cards   memory.Folder
	conv    convFold
	entries []memory.Entry
	at      map[string]int
	kept    *wordsPart
	pending []pendingDoc
}

type pendingDoc struct {
	doc    convDoc
	counts map[string]int
}

func newMemFold(id string, m memFile, kept *wordsPart) *memFold {
	f := &memFold{cards: memory.Folder{Session: id, State: m.State}, conv: m.Conv.clone(), entries: slices.Clone(m.Entries), at: map[string]int{}, kept: kept}
	for i, e := range f.entries {
		f.at[e.ID] = i
	}
	return f
}

// observe folds the next event, redacted again first, and says whether no
// turn is open after it.
func (f *memFold) observe(ev log.Event) (bool, error) {
	if len(ev.Payload) > 0 {
		var err error
		if ev.Payload, _, err = redact.JSON(ev.Payload, nil); err != nil {
			return false, err
		}
	}
	for _, e := range f.cards.Observe(ev) {
		// A card made again from the same events has the same ID.
		if i, ok := f.at[e.ID]; ok {
			f.entries[i] = e
		} else {
			f.at[e.ID] = len(f.entries)
			f.entries = append(f.entries, e)
		}
	}
	if d, fields, ok := f.conv.observe(ev); ok {
		counts := map[string]int{}
		for _, field := range fields {
			retrieval.Count(counts, field.Text, field.Weight)
		}
		f.pending = append(f.pending, pendingDoc{d, counts})
	}
	open, _ := f.cards.Open()
	return open == 0, nil
}

// keep moves the documents folded so far into the kept part.
func (f *memFold) keep() {
	for _, p := range f.pending {
		f.kept.add(p.doc, p.counts)
	}
	f.pending = nil
}

// result is the log's memory: the kept part, then the documents after it.
func (f *memFold) result(id string, last uint64) *logMemory {
	rest := newPart()
	for _, p := range f.pending {
		rest.add(p.doc, p.counts)
	}
	open, asked := f.cards.Open()
	return &logMemory{id: id, entries: f.entries, parts: []*wordsPart{f.kept, rest}, open: open, asked: asked, last: last}
}

// memory is session id's memory, its .mem brought to its last whole turn
// and the events after folded now, with the postings of the words keep
// allows (all for nil).
func (s Store) memory(id string, keep func(string) bool) (*logMemory, error) {
	path, err := s.path(id)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("log: %w", err)
	}
	defer f.Close()
	m, kept, words, loaded := loadMem(memPath(s.Dir, id), f, keep)
	if !loaded {
		m, kept, words = memFile{Version: memVersion}, newPart(), nil
	}
	out, err := s.extendMem(f, id, m, kept, words)
	if err != nil && loaded {
		// The log does not go on from where the file says it ends: made
		// again from the start, the log says whether it is the log that
		// is wrong.
		out, err = s.extendMem(f, id, memFile{Version: memVersion}, newPart(), nil)
	}
	return out, err
}

// extendMem folds in the events of f after the ones m covers, and writes
// the .mem again when the log gained whole turns. kept holds m's
// documents; words is its index's bytes, read whole before kept is added
// to, since a search reads only its own words.
func (s Store) extendMem(f *os.File, id string, m memFile, kept *wordsPart, words []byte) (*logMemory, error) {
	if _, err := f.Seek(m.End, io.SeekStart); err != nil {
		return nil, err
	}
	fold := newMemFold(id, m, kept)
	covered := m
	whole := words == nil
	base, previous, last := m.End, int64(0), m.Seq
	err := log.ReplayFrom(f, m.Seq+1, func(ev log.Event, line []byte, end int64) error {
		start := base + previous
		previous, last = end, ev.Seq
		closed, err := fold.observe(ev)
		if err != nil || !closed {
			return err
		}
		if !whole {
			index, err := retrieval.Decode(words, nil)
			if err != nil {
				return err
			}
			fold.kept.index, whole = index, true
		}
		fold.keep()
		covered.Seq, covered.Start, covered.End, covered.Hash = ev.Seq, start, base+end, lineHash(line)
		covered.State, covered.Conv = fold.cards.State, fold.conv.clone()
		return nil
	})
	if err != nil && !errors.Is(err, log.ErrTruncated) {
		return nil, err
	}
	if covered.Seq > m.Seq {
		covered.Version, covered.Entries, covered.Docs = memVersion, fold.entries, len(fold.kept.seqs)
		head, err := json.Marshal(covered)
		if err != nil {
			return nil, err
		}
		// Best effort, as the log's index is written.
		_ = writeDerivedBytes(s.Dir, id, ".mem", appendWords(append(head, '\n'), fold.kept))
	}
	return fold.result(id, last), nil
}

// loadMem reads the memory file at path when it matches the log f, with
// the postings of the words keep allows.
func loadMem(path string, f *os.File, keep func(string) bool) (memFile, *wordsPart, []byte, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return memFile{}, nil, nil, false
	}
	head, rest, ok := bytes.Cut(raw, []byte("\n"))
	var m memFile
	if !ok || json.Unmarshal(head, &m) != nil || m.Version != memVersion || m.Seq == 0 || !lineMatches(f, m.Seq, m.Start, m.End, m.Hash) {
		return memFile{}, nil, nil, false
	}
	kept, words, err := readWords(rest, keep)
	if err != nil || len(kept.seqs) != m.Docs {
		return memFile{}, nil, nil, false
	}
	return m, kept, words, true
}

// foldEvents is the memory of a log that is not a file, from its events.
func foldEvents(id string, events []log.Event) (*logMemory, error) {
	fold := newMemFold(id, memFile{}, newPart())
	last := uint64(0)
	for _, ev := range events {
		closed, err := fold.observe(ev)
		if err != nil {
			return nil, err
		}
		if closed {
			fold.keep()
		}
		last = ev.Seq
	}
	return fold.result(id, last), nil
}

// noWords reads a .mem's documents without their words' postings, for a
// recall, which needs no ranking.
func noWords(string) bool { return false }

// queryWords allows the words of query, for a search, which needs only
// their postings.
func queryWords(query string) func(string) bool {
	words := map[string]bool{}
	for _, w := range retrieval.Words(query) {
		words[w] = true
	}
	return func(w string) bool { return words[w] }
}
