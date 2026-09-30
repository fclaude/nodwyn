package core

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/fclaude/nodwyn/internal/extract"
	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/redact"
)

// Attachment text is capped: the head and the tail of a long document are
// kept, the middle is left out and said so. The whole file stays in the
// workspace for the model to read in ranges.
const (
	maxAttachText  = 48 << 10
	attachTextHead = 36 << 10
)

// Attach adds a file the person chose to the conversation (ADR 22 §3): it
// is copied into the workspace's .nodwyn/attachments, its text extracted by
// type, redacted at ingest and capped, and recorded as an attach record the
// next model request reads as data. The session must be idle; the file at
// path is only read. Images are refused until images can be sent.
func (s *Session) Attach(path, name, actor string) (log.Attach, error) {
	if s.Busy() {
		return log.Attach{}, errors.New("attach: wait until the current turn ends")
	}
	name = cleanName(name, path)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	result, err := extract.File(ctx, path, name)
	if err != nil {
		return log.Attach{}, err
	}
	id := strings.ToLower(rand.Text()[:10])
	rel := filepath.Join(".nodwyn", "attachments", id, name)
	dest := filepath.Join(s.opts.Repo, rel)
	size, sum, err := copyFile(path, dest)
	if err != nil {
		return log.Attach{}, fmt.Errorf("attach: keeping a copy: %w", err)
	}
	ignoreDir(filepath.Join(s.opts.Repo, ".nodwyn", "attachments"))
	clean := redact.Redact(result.Text, nil)
	var classes []string
	for _, e := range clean.Events {
		classes = append(classes, e.Class)
	}
	text, omitted := clean.Text, 0
	if len(text) > maxAttachText {
		head := cutRunes(text, attachTextHead)
		tail := text[len(text)-(maxAttachText-attachTextHead):]
		for len(tail) > 0 && !utf8Start(tail[0]) {
			tail = tail[1:]
		}
		omitted = len(text) - len(head) - len(tail)
		text = head + "\n[… the middle of the file is not shown …]\n" + tail
	}
	record := log.Attach{ID: id, Name: name, Type: result.Type, Bytes: size, SHA256: sum, Stored: filepath.ToSlash(rel), Pages: result.Pages, Text: text, Omitted: omitted, Redactions: classes, Actor: actor}
	if _, err := s.Record(log.KindAttach, record); err != nil {
		return log.Attach{}, err
	}
	record.Text = "" // the caller gets the receipt, not the content again
	return record, nil
}

// cleanName keeps a file name a person would recognize, without path
// separators or control characters.
func cleanName(name, path string) string {
	if name == "" {
		name = filepath.Base(path)
	}
	name = filepath.Base(filepath.Clean("/" + name))
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '/' || r == '\\' {
			return '_'
		}
		return r
	}, name)
	if name == "" || name == "." || name == "/" || strings.HasPrefix(name, ".") {
		name = "attachment" + strings.TrimLeft(name, ".")
	}
	if len(name) > 120 {
		ext := filepath.Ext(name)
		name = name[:120-len(ext)] + ext
	}
	return name
}

func copyFile(src, dest string) (int64, string, error) {
	in, err := os.Open(src)
	if err != nil {
		return 0, "", err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return 0, "", err
	}
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, "", err
	}
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, hash), io.LimitReader(in, extract.MaxInput+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > extract.MaxInput {
		err = errors.New("file grew past the attachment limit while copying")
	}
	if err != nil {
		os.Remove(dest)
		return 0, "", err
	}
	return n, hex.EncodeToString(hash.Sum(nil)), nil
}

// ignoreDir keeps nodwyn's own copies out of a repository's commits.
func ignoreDir(dir string) {
	path := filepath.Join(dir, ".gitignore")
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		_ = os.WriteFile(path, []byte("# nodwyn's copies of attached files\n*\n"), 0o600)
	}
}

func cutRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8Start(s[n]) {
		n--
	}
	return s[:n]
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }
