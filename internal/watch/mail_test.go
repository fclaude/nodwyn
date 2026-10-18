package watch

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/test/fakes/gmail"
)

func gmailSource(g *gmail.Gmail) Source {
	return Source{Kind: Gmail, Server: g.URL, Credential: "google", Query: "from:(@acme.com)"}
}

// Gmail's messages become one item per thread: the thread's id, with its
// newest message's id as the version, so a new message makes a change and a
// message read or archived does not; content is fetched only for a thread
// that is new or changed.
func TestGmailThreadsAreItemsWhoseVersionIsTheirNewestMessage(t *testing.T) {
	g := gmail.New("ya29.token")
	defer g.Close()
	g.Set(
		gmail.Message{ID: "m3", Thread: "t2", From: "Bo <bo@acme.com>", To: "me@example.com", Subject: "Invoice 88", MessageID: "<m3@acme>", Date: "Fri, 16 Oct 2026 10:00:00 -0700", Body: "Please pay by Friday.", Labels: []string{"INBOX", "UNREAD"}},
		gmail.Message{ID: "m2", Thread: "t1", From: "Ana <ana@acme.com>", To: "me@example.com", Subject: "Re: Contract", Body: "Signed, thanks.", Labels: []string{"INBOX"}},
		gmail.Message{ID: "m1", Thread: "t1", From: "me@example.com", To: "ana@acme.com", Subject: "Contract", Body: "Here it is."},
	)
	store := keys(t, "google", "ya29.token")
	p := pollerWith(store, boundFor(t, "GET", "google", "127.0.0.1", "header:Authorization:Bearer"))
	s := gmailSource(g)
	items, err := p.Poll(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].ID != "t2" || items[0].Version != "m3" || items[1].ID != "t1" || items[1].Version != "m2" {
		t.Fatalf("%+v", items)
	}
	first := items[0]
	if first.Title != "Invoice 88" || first.Fields["from"] != "Bo <bo@acme.com>" || first.Fields["labels"] != "INBOX, UNREAD" || first.Fields["message_id"] != "<m3@acme>" || first.Body != "Please pay by Friday." {
		t.Fatalf("%+v", first)
	}
	if first.Fields["reply_to"] != "Bo <bo@acme.com>" || !strings.Contains(first.URL, "t2") {
		t.Fatalf("%+v", first)
	}
	// What is already seen is not fetched again; a new message in it is.
	look := Look{Seen: map[string]string{"t2": "m3", "t1": "m2"}}
	items, err = p.PollLook(context.Background(), s, look)
	if err != nil || items[0].Title != "" || items[0].Body != "" || items[0].Version != "m3" {
		t.Fatalf("a seen thread was read again: %+v %v", items, err)
	}
	g.Set(append([]gmail.Message{{ID: "m4", Thread: "t1", From: "Ana <ana@acme.com>", Subject: "Re: Contract", Body: "One more thing."}}, gmail.Message{ID: "m3", Thread: "t2", Subject: "Invoice 88", Body: "Please pay by Friday."}, gmail.Message{ID: "m2", Thread: "t1"}, gmail.Message{ID: "m1", Thread: "t1"})...)
	items, err = p.PollLook(context.Background(), s, look)
	if err != nil {
		t.Fatal(err)
	}
	changes := Changes(look.Seen, items, nil)
	if len(changes) != 1 || changes[0].Kind != "updated" || changes[0].Item.ID != "t1" || changes[0].Item.Body != "One more thing." {
		t.Fatalf("%+v", changes)
	}
	if w := changes[0].Summary(s); w != "New reply from Ana <ana@acme.com>: Re: Contract" {
		t.Fatal(w)
	}
	// A poll that only records fetches no message at all.
	g.Set(gmail.Message{ID: "m9", Thread: "t9", Subject: "x"})
	if items, err = p.PollLook(context.Background(), s, Look{IDsOnly: true}); err != nil || len(items) != 1 || items[0].Title != "" || items[0].ID != "t9" {
		t.Fatalf("%+v %v", items, err)
	}
	if text := (Event{items[0], "opened"}).Text(s); !strings.Contains(text, "Mailbox: Gmail, thread t9") {
		t.Fatal(text)
	}
}

// The credential reads Gmail only where the table binds it for GET, and a
// refusal by Gmail is said with what it most likely means.
func TestGmailIsReadOnlyWhereBoundAndRefusalsAreExplained(t *testing.T) {
	g := gmail.New("ya29.token")
	defer g.Close()
	g.Set(gmail.Message{ID: "m1", Thread: "t1", Subject: "x"})
	s := gmailSource(g)
	if _, err := pollerWith(keys(t, "google", "ya29.token"), boundFor(t, "POST", "google", "127.0.0.1", "header:Authorization:Bearer")).Poll(context.Background(), s); err == nil || !strings.Contains(err.Error(), "is not bound") {
		t.Fatalf("read with only a POST bound: %v", err)
	}
	lists := g.Lists()
	if g.Lists() != lists {
		t.Fatal("a refused read reached Gmail")
	}
	if _, err := pollerWith(keys(t, "google", "another-token"), bound(t, "google", "127.0.0.1", "header:Authorization:Bearer")).Poll(context.Background(), s); err == nil || !strings.Contains(err.Error(), "may have expired or been revoked") {
		t.Fatalf("a 401 is not explained: %v", err)
	}
	g.Forbid(true)
	if _, err := pollerWith(keys(t, "google", "ya29.token"), bound(t, "google", "127.0.0.1", "header:Authorization:Bearer")).Poll(context.Background(), s); err == nil || !strings.Contains(err.Error(), "gmail.readonly scope") {
		t.Fatalf("a 403 is not explained: %v", err)
	}
	post := pollerWith(keys(t, "google", "ya29.token"), boundFor(t, "POST", "google", "127.0.0.1", "header:Authorization:Bearer"))
	if err := post.SaveDraft(context.Background(), s, Reply{To: "ana@acme.com", Subject: "x", Thread: "t1"}, "x"); err == nil || !strings.Contains(err.Error(), "gmail.compose scope") {
		t.Fatalf("a refused draft is not explained: %v", err)
	}
	g.Forbid(false)
	for _, bad := range []Source{{Kind: Gmail}, {Kind: Gmail, Credential: "g", Query: strings.Repeat("a", 501)}, {Kind: Gmail, Credential: "g", Server: "ftp://x"}} {
		if bad.Validate() == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}

// A draft joins the thread, answers the message it replies to, goes to the
// sender, and is built from a stranger's headers without letting them add
// any: a sender who puts line breaks in a name or a subject adds no header.
func TestADraftReplyJoinsTheThreadAndAddsNoHeader(t *testing.T) {
	g := gmail.New("ya29.token")
	defer g.Close()
	s := gmailSource(g)
	post := pollerWith(keys(t, "google", "ya29.token"), boundFor(t, "POST", "google", "127.0.0.1", "header:Authorization:Bearer"))
	hostile := Item{ID: "t7", Title: "Quote\r\nBcc: spy@evil.example", Fields: map[string]string{"reply_to": "Ana <ana@acme.com>", "message_id": "<m7@acme>\r\nBcc: spy@evil.example"}}
	reply, err := ReplyTo(hostile)
	if err != nil || reply.To != `"Ana" <ana@acme.com>` && reply.To != "Ana <ana@acme.com>" {
		t.Fatalf("%+v %v", reply, err)
	}
	if err := post.SaveDraft(context.Background(), s, reply, "Thanks, Ana: Friday works.\n\nÅngström"); err != nil {
		t.Fatal(err)
	}
	drafts := g.Drafts()
	if len(drafts) != 1 || drafts[0].Thread != "t7" || drafts[0].Auth != "Bearer ya29.token" {
		t.Fatalf("%+v", drafts)
	}
	raw := drafts[0].Raw
	head, body, _ := strings.Cut(raw, "\r\n\r\n")
	for _, line := range strings.Split(head, "\r\n") {
		name, _, _ := strings.Cut(line, ":")
		switch name {
		case "To", "Subject", "In-Reply-To", "References", "MIME-Version", "Content-Type", "Content-Transfer-Encoding":
		default:
			t.Fatalf("a header was added: %q in\n%s", line, head)
		}
	}
	if !strings.Contains(head, "To: ") || !strings.Contains(head, "ana@acme.com") || !strings.Contains(head, "Subject: Re: QuoteBcc: spy@evil.example") || !strings.Contains(head, "In-Reply-To: <m7@acme>Bcc: spy@evil.example") {
		t.Fatalf("%s", head)
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(body, "\r\n", ""))
	if err != nil || string(decoded) != "Thanks, Ana: Friday works.\n\nÅngström" {
		t.Fatalf("%q %v", decoded, err)
	}
	// Not an address, not a draft; only where POST is bound; only for Gmail.
	if _, err := ReplyTo(Item{Fields: map[string]string{"reply_to": "not an address"}}); err == nil {
		t.Fatal("a reply to no address")
	}
	if err := pollerWith(keys(t, "google", "ya29.token"), bound(t, "google", "127.0.0.1", "header:Authorization:Bearer")).SaveDraft(context.Background(), s, reply, "x"); err == nil || !strings.Contains(err.Error(), "method POST") {
		t.Fatalf("a draft was saved with only the read bound: %v", err)
	}
	if err := post.SaveDraft(context.Background(), Source{Kind: GiteaPulls, Server: "https://x", Repo: "a/b", Credential: "k"}, reply, "x"); err == nil {
		t.Fatal("a draft in a forge")
	}
	if len(g.Drafts()) != 1 {
		t.Fatal("a refused draft reached Gmail")
	}
}
