package plain

import (
	"bytes"
	"testing"

	"github.com/fclaude/nibb/internal/event"
)

func TestThinkingPrefixedPerLineNotPerFragment(t *testing.T) {
	var out bytes.Buffer
	c := &Chat{Out: &out, ShowThinking: true}
	for _, frag := range []string{"let me", " think\nabout", " it", "\n"} {
		c.show(event.Event{Kind: event.StreamThinking, Payload: event.Text{Text: frag}})
	}
	for _, frag := range []string{"The ans", "wer."} {
		c.show(event.Event{Kind: event.StreamText, Payload: event.Text{Text: frag}})
	}
	c.show(event.Event{Kind: event.Status, Payload: event.StatusInfo{State: "idle"}})
	want := "· let me think\n· about it\nThe answer.\n"
	if out.String() != want {
		t.Fatalf("got %q\nwant %q", out.String(), want)
	}
}

func TestChatStreamsAreIndependent(t *testing.T) {
	var first, second bytes.Buffer
	a := &Chat{Out: &first, ShowThinking: true}
	b := &Chat{Out: &second, ShowThinking: true}
	a.writeStream("thinking", "first")
	b.writeStream("thinking", "second")
	a.writeStream("thinking", " continued")
	b.show(event.Event{Kind: event.Status, Payload: event.StatusInfo{State: "idle"}})
	a.writeStream("text", "answer")
	a.show(event.Event{Kind: event.Status, Payload: event.StatusInfo{State: "idle"}})
	if got := first.String(); got != "· first continued\nanswer\n" {
		t.Fatalf("first stream: %q", got)
	}
	if got := second.String(); got != "· second\n" {
		t.Fatalf("second stream: %q", got)
	}
}
