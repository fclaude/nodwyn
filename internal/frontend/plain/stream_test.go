package plain

import (
	"bytes"
	"testing"

	"github.com/fclaude/nibb/internal/event"
)

func TestThinkingPrefixedPerLineNotPerFragment(t *testing.T) {
	var out bytes.Buffer
	c := &Chat{Out: &out, ShowThinking: true}
	stream.mode, stream.atLineStart = "", true
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
