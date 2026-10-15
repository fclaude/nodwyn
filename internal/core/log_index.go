package core

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/redact"
)

// A session log's index (ADR 49 §10) is what listing the session and reading
// a page of its log need, derived from the log and kept beside it as
// <id>.idx, so that neither reads the whole log again each time it grows: a
// task that runs every day for months keeps one log of gigabytes. It is a
// revocable view of the log (SPEC, invariant 1), and nothing is recorded only
// in it: a reader checks the line the index ends at, and every line it
// starts reading at, against the log, and builds the index again from the
// log on any mismatch, so deleting it is always safe. Logs only grow: their
// one repair, log.Open cutting a torn last line, cuts beyond the last
// complete event, which is as far as an index goes.
//
// It is written by whichever nodwyn process reads the log, after folding in
// what the log gained, as a temporary file renamed into place; two writers
// race harmlessly, since each wrote an index of the same log, and the one
// that covers less is folded forward by the next read. It lives in nodwyn's
// data directory, beside the log, where the model's file primitives cannot
// reach (the default denylist), and whatever could write it could write the
// log itself.

const indexVersion = 1

// markEvery is how many events apart the index notes where a line starts: a
// page is read from the nearest mark before it, at most this many events
// early.
const markEvery = 256

type logIndex struct {
	Version int `json:"version"`
	// The last event folded in: its seq, where its line starts and ends in
	// the log, and the SHA-256 of the line.
	Seq   uint64 `json:"seq"`
	Start int64  `json:"start"`
	End   int64  `json:"end"`
	Hash  string `json:"hash"`
	// Fold is the session's listing up to and including Seq.
	Fold infoFold `json:"fold"`
	// Marks[k] is where the line of event k*markEvery+1 starts.
	Marks []int64 `json:"marks"`
}

func indexPath(dir, id string) string { return filepath.Join(dir, id+".idx") }

// index is the session's index brought to the end of its log: <id>.idx when
// it still matches the log, else one built from the start, with the events
// the log gained since folded in, and written back when it grew. truncated
// reports a partial last line, which no index covers.
func (s Store) index(id string) (logIndex, bool, error) {
	path, err := s.path(id)
	if err != nil {
		return logIndex{}, false, err
	}
	f, err := os.Open(path)
	if err != nil {
		return logIndex{}, false, fmt.Errorf("log: %w", err)
	}
	defer f.Close()
	idx, loaded := loadIndex(indexPath(s.Dir, id), f)
	if !loaded {
		idx = freshIndex(id)
	}
	result, truncated, err := extendIndex(f, id, idx)
	if err != nil && loaded {
		// The log does not go on from where the index says it ends: the
		// index is not this log's. Built again from the start, the log
		// says whether it is the log that is wrong.
		result, truncated, err = extendIndex(f, id, freshIndex(id))
	}
	if err != nil {
		return logIndex{}, false, err
	}
	if _, err := result.Fold.result(id); err != nil {
		return logIndex{}, false, err
	}
	if result.Seq > idx.Seq {
		// Best effort: a directory nodwyn cannot write to only means the
		// next read folds the same events in again.
		_ = saveIndex(s.Dir, id, result)
	}
	return result, truncated, nil
}

func freshIndex(id string) logIndex {
	return logIndex{Version: indexVersion, Fold: infoFold{Info: SessionInfo{ID: id}}}
}

// extendIndex folds in the events of f after the ones idx covers.
func extendIndex(f *os.File, id string, idx logIndex) (logIndex, bool, error) {
	if _, err := f.Seek(idx.End, io.SeekStart); err != nil {
		return logIndex{}, false, err
	}
	base, previous := idx.End, int64(0)
	err := log.ReplayFrom(f, idx.Seq+1, func(ev log.Event, line []byte, end int64) error {
		if err := idx.Fold.observe(id, ev); err != nil {
			return err
		}
		// A line starts where the one before it ended: a blank line between
		// them is skipped by whoever reads from there.
		start := base + previous
		previous = end
		if (ev.Seq-1)%markEvery == 0 {
			idx.Marks = append(idx.Marks, start)
		}
		sum := sha256.Sum256(line)
		idx.Seq, idx.Start, idx.End, idx.Hash = ev.Seq, start, base+end, hex.EncodeToString(sum[:])
		return nil
	})
	truncated := errors.Is(err, log.ErrTruncated)
	if err != nil && !truncated {
		return logIndex{}, false, err
	}
	return idx, truncated, nil
}

// loadIndex reads the index at path when it matches the log f: the line it
// says it ends at is in f, whole, with its seq and hash. Anything else is
// no index.
func loadIndex(path string, f *os.File) (logIndex, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return logIndex{}, false
	}
	var idx logIndex
	if json.Unmarshal(raw, &idx) != nil || idx.Version != indexVersion || idx.Seq == 0 || idx.Start < 0 || idx.End <= idx.Start ||
		uint64(len(idx.Marks)) != (idx.Seq-1)/markEvery+1 || idx.Marks[0] != 0 {
		return logIndex{}, false
	}
	line := make([]byte, idx.End-idx.Start)
	if _, err := f.ReadAt(line, idx.Start); err != nil || line[len(line)-1] != '\n' {
		return logIndex{}, false
	}
	line = bytes.TrimSpace(line)
	sum := sha256.Sum256(line)
	var ev log.Event
	if hex.EncodeToString(sum[:]) != idx.Hash || json.Unmarshal(line, &ev) != nil || ev.Seq != idx.Seq {
		return logIndex{}, false
	}
	return idx, true
}

// saveIndex writes idx beside the log, whole or not at all.
func saveIndex(dir, id string, idx logIndex) error {
	raw, err := json.Marshal(idx)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, id+".idx.*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), indexPath(dir, id))
}

// errPageFull ends a page's read once it holds what it asked for.
var errPageFull = errors.New("core: page full")

// readPage reads a page of the log from the mark before it: the events after
// after, through through, at most limit of them, redacted for display. A
// mark that is not where its event's line starts is an index that is not
// this log's: it is removed, and the caller reads again.
func (s Store) readPage(id string, idx logIndex, page *LogPage, after, through uint64, limit int) error {
	k := after / markEvery
	path, err := s.path(id)
	if err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("log: %w", err)
	}
	defer f.Close()
	if _, err := f.Seek(idx.Marks[k], io.SeekStart); err != nil {
		return err
	}
	read := false
	err = log.ReplayFrom(f, k*markEvery+1, func(ev log.Event, _ []byte, _ int64) error {
		read = true
		if ev.Seq <= after {
			return nil
		}
		if ev.Seq > through || len(page.Events) == limit {
			return errPageFull
		}
		if len(ev.Payload) > 0 {
			var err error
			if ev.Payload, _, err = redact.JSON(ev.Payload, nil); err != nil {
				return err
			}
		}
		page.Events = append(page.Events, ev)
		page.Next = ev.Seq
		return nil
	})
	if err != nil && !read && !errors.Is(err, log.ErrTruncated) {
		// Not even the mark's own line was there.
		os.Remove(indexPath(s.Dir, id))
		return errStaleIndex
	}
	if err != nil && !errors.Is(err, errPageFull) && !errors.Is(err, log.ErrTruncated) {
		return err
	}
	return nil
}

// errStaleIndex says an index's mark did not match its log.
var errStaleIndex = errors.New("core: the log's index did not match it")
