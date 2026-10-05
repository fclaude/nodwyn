package core

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/fclaude/nodwyn/internal/extract"
	"github.com/fclaude/nodwyn/internal/guard"
	"github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/loop"
	"github.com/fclaude/nodwyn/internal/redact"
	"github.com/fclaude/nodwyn/internal/window"
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
// next model request reads as data. An image is attached as an image, after
// its own confirm (attachImage). The session must be idle, and stays so
// until the attachment is done; the file at path is only read.
func (s *Session) Attach(path, name, actor string) (log.Attach, error) {
	ctx, done, err := s.beginAttach()
	if err != nil {
		return log.Attach{}, err
	}
	defer done()
	name = cleanName(name, path)
	if extract.IsImage(path, name) {
		return s.attachImage(ctx, path, name, actor)
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	result, err := extract.File(ctx, path, name)
	if err != nil {
		return log.Attach{}, err
	}
	id, rel, dest := s.attachmentPath(name)
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
	record := log.Attach{ID: id, Name: name, Type: result.Type, Bytes: size, SHA256: sum, Stored: rel, Pages: result.Pages, Text: text, Omitted: omitted, Redactions: classes, Actor: actor}
	if _, err := s.Record(log.KindAttach, record); err != nil {
		return log.Attach{}, err
	}
	record.Text = "" // the caller gets the receipt, not the content again
	return record, nil
}

// imageConfirmWait is how long an image waits for its confirm; unanswered,
// it is not attached.
const imageConfirmWait = 10 * time.Minute

// attachImage attaches an image (ADR 22 §3, amended 2026-10-01). Its pixels
// cannot be redacted, so it is attached only when the person says yes to a
// security confirm that names the file, says so, and names where it will
// go: the control model's host, on this machine or another. --yolo never
// answers that yes and --no-confirm declines it. The image must be one the
// control model takes. It is copied into the attachment store, exactly the
// bytes that were checked, and recorded without them: its type, size,
// digest, dimensions, token estimate and the confirmed host.
func (s *Session) attachImage(ctx context.Context, path, name, actor string) (log.Attach, error) {
	data, img, err := extract.ReadImage(path, name)
	if err != nil {
		return log.Attach{}, err
	}
	asking, cancel := context.WithTimeout(ctx, 15*time.Second)
	vision, err := s.loop.TakesImages(asking, loop.ControlModel)
	cancel()
	if err != nil {
		return log.Attach{}, fmt.Errorf("attach: could not learn whether the control model takes images: %w", err)
	}
	if !vision {
		return log.Attach{}, s.noVision(name)
	}
	digest := sha256.Sum256(data)
	tokens := window.ImageTokens(img.Width, img.Height)
	control := s.currentModel()
	host := modelHost(control.URL)
	waiting, cancel := context.WithTimeout(ctx, imageConfirmWait)
	defer cancel()
	approval, err := s.confirmImage(waiting, imagePrompt(name, img, len(data), tokens, control.Model, host))
	if err != nil || !approval.Allowed {
		why := s.declined(err)
		if _, recErr := s.Record(log.KindNote, log.Note{Text: fmt.Sprintf("image not attached: %s, %s", name, why)}); recErr != nil {
			return log.Attach{}, recErr
		}
		return log.Attach{}, fmt.Errorf("attach: %s is not attached: its confirm was %s", name, why)
	}
	id, rel, dest := s.attachmentPath(name)
	if err := writeNew(dest, data); err != nil {
		return log.Attach{}, fmt.Errorf("attach: keeping a copy: %w", err)
	}
	ignoreDir(filepath.Join(s.opts.Repo, ".nodwyn", "attachments"))
	record := log.Attach{ID: id, Name: name, Type: "image", Bytes: int64(len(data)), SHA256: hex.EncodeToString(digest[:]), Stored: rel,
		MediaType: img.MediaType, Width: img.Width, Height: img.Height, Tokens: tokens, Destination: host,
		ApprovedBy: approval.Actor, PromptID: approval.PromptID, Actor: actor}
	if _, err := s.Record(log.KindAttach, record); err != nil {
		os.Remove(dest)
		return log.Attach{}, err
	}
	return record, nil
}

// confirmImage asks an image's confirm the way the session answers any
// security confirm: a session that answers every confirm with no
// (--no-confirm) declines it, one that answers ordinary confirms itself
// (--yolo, a run's auto_approve) declines it too, as guard.AutoAllow does,
// and otherwise the front-end asks the person.
func (s *Session) confirmImage(ctx context.Context, p guard.Prompt) (guard.Approval, error) {
	switch {
	case s.opts.NoConfirm:
		return guard.Approval{}, nil
	case s.opts.AutoApprove:
		allowed, err := guard.AutoAllow.Confirm(ctx, p)
		return guard.Approval{Allowed: allowed}, err
	case s.opts.JudgeApprove || s.opts.Unattended:
		// Nobody to ask, and the judge never answers a security confirm.
		return guard.Approval{}, nil
	}
	return s.ConfirmApproval(ctx, p)
}

// declined says why an image's confirm did not allow it.
func (s *Session) declined(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Sprintf("not answered within %d minutes", int(imageConfirmWait.Minutes()))
	case err != nil:
		return "canceled"
	case s.opts.NoConfirm:
		return "declined: this session answers every confirm with no"
	case s.opts.AutoApprove, s.opts.JudgeApprove, s.opts.Unattended:
		return "declined: a session that answers confirms itself never answers a security confirm"
	}
	return "declined"
}

// imagePrompt is an image's confirm: the file, that its contents cannot be
// redacted, and where it goes.
func imagePrompt(name string, img extract.Image, size, tokens int, model, host string) guard.Prompt {
	where := fmt.Sprintf("It goes to the control model %s at %s, another machine: its pixels leave this computer as they are.", model, host)
	switch {
	case host == "":
		where = fmt.Sprintf("It goes to the control model %s, whose host config.toml does not name.", model)
	case loopbackHost(host):
		where = fmt.Sprintf("It goes to the control model %s at %s, on this machine.", model, host)
	}
	return guard.Prompt{
		Security: true,
		Prim:     "attach image",
		Args:     name,
		Reasons: []string{
			fmt.Sprintf("%s is an image (%s, %d×%d, %s, about %s tokens). Its contents cannot be redacted: whatever it shows, a password or a key included, reaches the model as it is, and nothing in it is read as text.", name, img.Format, img.Width, img.Height, sizeLabel(size), thousands(tokens)),
			where,
			"It goes with later requests to that host only, while the conversation shows it; the judge never gets it.",
		},
	}
}

// noVision says why an image cannot be attached to this session, and how
// to change that.
func (s *Session) noVision(name string) error {
	c := s.currentModel()
	switch {
	case c.URL == "" || c.Model == "":
		return fmt.Errorf("attach: %s is an image, and no control model is configured to take it; add [control] to %s with a model that takes images", name, s.opts.Paths.ConfigFile)
	case c.Provider == "openai":
		return fmt.Errorf("attach: %s is an image, and the model %s is set as taking no images; if it does, set Takes images to Yes for it in Settings › Models (vision = false in its entry in %s says it does not), or choose a model that does", name, c.Model, s.opts.Paths.ConfigFile)
	}
	return fmt.Errorf("attach: %s is an image, and the control model %s does not take images: ollama does not list vision among its capabilities (ollama show %s); choose a vision model for [control] in %s", name, c.Model, c.Model, s.opts.Paths.ConfigFile)
}

// loopbackHost reports whether host (host or host:port) is this machine.
func loopbackHost(host string) bool {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func sizeLabel(n int) string {
	if n < 1<<20 {
		return fmt.Sprintf("%d KB", (n+1023)>>10)
	}
	return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
}

// thousands writes n with commas: 1,105.
func thousands(n int) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// attachmentPath names a new attachment's copy: an id, its path relative to
// the workspace root as the log records it, and its path on disk.
func (s *Session) attachmentPath(name string) (id, rel, dest string) {
	id = strings.ToLower(rand.Text()[:10])
	rel = filepath.Join(".nodwyn", "attachments", id, name)
	return id, filepath.ToSlash(rel), filepath.Join(s.opts.Repo, rel)
}

// writeNew writes data to a new private file.
func writeNew(dest string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return err
	}
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = out.Write(data)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(dest)
	}
	return err
}

// beginAttach holds the session for one attachment: no turn starts and no
// other attachment begins until done is called, and Abort or Close cancels
// it, an image's confirm included.
func (s *Session) beginAttach() (context.Context, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.closed || s.closing:
		return nil, nil, errors.New("core: session is closed")
	case s.busy:
		return nil, nil, errors.New("attach: wait until the current turn ends")
	case s.attaching:
		return nil, nil, errors.New("attach: another attachment is in progress; wait until it is done")
	case s.switching:
		return nil, nil, errors.New("attach: the model is being switched; wait until it is done")
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.attaching, s.attachCancel = true, cancel
	s.turns.Add(1)
	return ctx, func() {
		cancel()
		s.mu.Lock()
		s.attaching, s.attachCancel = false, nil
		s.mu.Unlock()
		s.turns.Done()
	}, nil
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
