package tui

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	fake "github.com/fclaude/nodwyn/test/fakes/ollama"
)

// attachOptions is a session on a model that takes images, with a text file
// and an image to attach.
func attachTest(t *testing.T) (m *model, text, img string) {
	t.Helper()
	vision := fake.NewT(t)
	t.Cleanup(vision.Close)
	vision.SetCapabilities("completion", "vision")
	o := testOptions(t)
	o.Config.Control.URL, o.Config.Control.Model = vision.URL, "fake-vision"
	b := testBackend(t, o)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	snap, err := b.Attach(ctx, o.Repo, "")
	if err != nil {
		t.Fatal(err)
	}
	m = newModel(ctx, b, o.Repo, snap.ID, false)
	m.restore(snap)
	dir := t.TempDir()
	text = filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(text, []byte("the plan is in the notes"), 0o600); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewGray(image.Rect(0, 0, 20, 10))); err != nil {
		t.Fatal(err)
	}
	img = filepath.Join(dir, "board.png")
	if err := os.WriteFile(img, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return m, text, img
}

// attachInBackground submits :attach and runs the attachment beside the
// keyboard, as Bubble Tea runs a command, so its confirm can be answered.
func attachInBackground(t *testing.T, m *model, path string) chan tea.Msg {
	t.Helper()
	m.composer.SetValue(":attach " + path)
	cmd := m.submit()
	if cmd == nil || !m.attaching {
		t.Fatalf(":attach did not start: %q", m.status)
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	return done
}

func finished(t *testing.T, m *model, done chan tea.Msg) {
	t.Helper()
	select {
	case msg := <-done:
		m.Update(msg)
	case <-time.After(10 * time.Second):
		t.Fatal("the attachment did not finish")
	}
}

// :attach adds a file to the conversation; an image asks its own confirm,
// which is answered while the attachment waits for it. Over the daemon that
// needs a connection of its own, since one connection's calls run in order.
func TestAttachThroughTheDaemon(t *testing.T) {
	m, text, img := attachTest(t)
	done := attachInBackground(t, m, text)
	finished(t, m, done)
	if m.attaching || m.status != "" {
		t.Fatalf("text: %q", m.status)
	}
	pump(t, m, transcriptHas(m, "[attached notes.txt · text"))

	done = attachInBackground(t, m, img)
	pump(t, m, func() bool { return m.prompt != nil })
	if m.prompt.title != "attach image" || !strings.Contains(m.prompt.detail, "cannot be redacted") {
		t.Fatalf("image confirm: %q\n%s", m.prompt.title, m.prompt.detail)
	}
	if title, _, _ := strings.Cut(m.View().Content, "\n"); !strings.Contains(title, "NEEDS YOUR ANSWER") {
		t.Fatalf("the confirm is not in front: %q", title)
	}
	m.promptInput.SetValue("y")
	m.Update(m.submit()())
	finished(t, m, done)
	if m.attaching || m.status != "" {
		t.Fatalf("image: %q", m.status)
	}
	pump(t, m, transcriptHas(m, "[attached board.png · image · 20×10"))
	// The confirm marked the session busy, as every prompt does; the
	// attachment's end must not leave it so, or the next message is
	// refused.
	if m.busy {
		t.Fatal("still busy after the attachment")
	}
}

// Ctrl+C while an image waits for its confirm cancels the attachment, as it
// aborts a turn, instead of quitting.
func TestCtrlCCancelsAnAttachment(t *testing.T) {
	m, _, img := attachTest(t)
	done := attachInBackground(t, m, img)
	pump(t, m, func() bool { return m.prompt != nil })
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal("Ctrl+C did nothing")
	}
	if _, quit := cmd().(tea.QuitMsg); quit {
		t.Fatal("Ctrl+C quit while an attachment waited")
	}
	finished(t, m, done)
	if m.attaching || !strings.HasPrefix(m.status, "Not attached: board.png") {
		t.Fatalf("after Ctrl+C: %q", m.status)
	}
}

// What :attach names is read as a shell reads it, and nothing is attached
// while a turn runs.
func TestAttachPathAndRefusals(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if p, err := attachPath(" ~/notes/a b.txt "); err != nil || p != filepath.Join(home, "notes", "a b.txt") {
		t.Fatalf("home: %q %v", p, err)
	}
	wd, _ := os.Getwd()
	if p, err := attachPath("notes.txt"); err != nil || p != filepath.Join(wd, "notes.txt") {
		t.Fatalf("relative: %q %v", p, err)
	}
	if _, err := attachPath("  "); err == nil {
		t.Fatal("an empty path was taken")
	}
	m := testModel()
	m.busy = true
	m.composer.SetValue(":attach notes.txt")
	if m.submit() != nil || m.attaching || !strings.Contains(m.status, "idle") {
		t.Fatalf("attached while busy: %q", m.status)
	}
	m.busy, m.attaching = false, true
	m.composer.SetValue("hello")
	if m.submit() != nil || m.composer.Value() != "hello" {
		t.Fatal("sent while attaching, or lost the draft")
	}
}
