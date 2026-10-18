package watch

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"net/mail"
	"net/url"
	"regexp"
	"strings"
)

// maxMessages is how many of the newest messages one check looks at.
const maxMessages = 25

// maxMailBody bounds the text of a message an event carries.
const maxMailBody = 20 << 10

// mailBase is the server a Gmail source asks.
func mailBase(s Source) string {
	if s.Server != "" {
		return strings.TrimRight(s.Server, "/")
	}
	return GmailAPI
}

// mail reads the newest messages of a Gmail search and gives one item per
// thread: the thread's id, and as its version the id of its newest message in
// the search. A message's id never changes and a new one has a new id, so
// reading or archiving a message does not make an event, as a thread's own
// history id would. A message's content is fetched only for a thread that is
// new or has a new message, and not at all for a poll that only records.
func (p Poller) mail(ctx context.Context, s Source, look Look) ([]Item, error) {
	query := s.Query
	if strings.TrimSpace(query) == "" {
		query = "in:inbox"
	}
	address := mailBase(s) + "/gmail/v1/users/me/messages?maxResults=" + fmt.Sprint(maxMessages) + "&q=" + url.QueryEscape(query)
	raw, err := p.send(ctx, s, "GET", address, "header:Authorization:Bearer", nil)
	if err != nil {
		return nil, mailError(err)
	}
	var list struct {
		Messages []struct {
			ID       string `json:"id"`
			ThreadID string `json:"threadId"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("the Gmail API did not answer with messages: %w", err)
	}
	var items []Item
	seen := map[string]bool{}
	for _, m := range list.Messages {
		if m.ThreadID == "" || seen[m.ThreadID] {
			continue
		}
		seen[m.ThreadID] = true
		item := Item{ID: m.ThreadID, Version: m.ID, Fields: map[string]string{}}
		if !look.IDsOnly && look.Seen[m.ThreadID] != m.ID {
			if err := p.message(ctx, s, m.ID, &item); err != nil {
				return nil, err
			}
		}
		items = append(items, item)
	}
	return items, nil
}

// mailError says what a refusal of Gmail most likely means: a credential
// that has expired or been revoked, or one authorized without the scope the
// call needs.
func mailError(err error) error {
	switch {
	case strings.Contains(err.Error(), "401"):
		return fmt.Errorf("%w: Gmail refused the credential, which may have expired or been revoked (nodwyn credential login ID authorizes it again)", err)
	case strings.Contains(err.Error(), "403"):
		return fmt.Errorf("%w: the credential needs the gmail.readonly scope (nodwyn credential add --oauth2 google --scopes gmail.readonly), and Gmail's API turned on in its Google project", err)
	}
	return err
}

type mailPart struct {
	MimeType string `json:"mimeType"`
	Body     struct {
		Data string `json:"data"`
	} `json:"body"`
	Parts []mailPart `json:"parts"`
}

type mailMessage struct {
	ID       string   `json:"id"`
	ThreadID string   `json:"threadId"`
	Labels   []string `json:"labelIds"`
	Snippet  string   `json:"snippet"`
	Payload  struct {
		mailPart
		Headers []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"headers"`
	} `json:"payload"`
}

// message fills in an item from one message: its headers as the fields
// filters read (from, to, subject, date, labels, and the reply-to address and
// message id a reply needs) and its text.
func (p Poller) message(ctx context.Context, s Source, id string, item *Item) error {
	raw, err := p.send(ctx, s, "GET", mailBase(s)+"/gmail/v1/users/me/messages/"+url.PathEscape(id)+"?format=full", "header:Authorization:Bearer", nil)
	if err != nil {
		return mailError(err)
	}
	var m mailMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return fmt.Errorf("the Gmail API did not answer with a message: %w", err)
	}
	header := map[string]string{}
	for _, h := range m.Payload.Headers {
		if _, ok := header[strings.ToLower(h.Name)]; !ok {
			header[strings.ToLower(h.Name)] = h.Value
		}
	}
	item.Title = header["subject"]
	item.URL = "https://mail.google.com/mail/u/0/#all/" + m.ThreadID
	item.Fields["from"] = header["from"]
	item.Fields["to"] = header["to"]
	item.Fields["date"] = header["date"]
	item.Fields["labels"] = strings.Join(m.Labels, ", ")
	item.Fields["reply_to"] = cmpOr(header["reply-to"], header["from"])
	item.Fields["message_id"] = header["message-id"]
	body := strings.TrimSpace(mailText(m.Payload.mailPart))
	if body == "" {
		body = m.Snippet
	}
	if len(body) > maxMailBody {
		body = strings.TrimSpace(body[:maxMailBody]) + "\n[The rest of the message is not shown here.]"
	}
	item.Body = body
	return nil
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

var tags = regexp.MustCompile(`(?s)<(script|style)[^>]*>.*?</(script|style)>|<[^>]+>`)

// mailText is the readable text of a message: its text/plain part, or, of a
// message with only HTML, the text of that with its tags taken out.
func mailText(p mailPart) string {
	if plain := findPart(p, "text/plain"); plain != "" {
		return plain
	}
	if page := findPart(p, "text/html"); page != "" {
		return strings.TrimSpace(html.UnescapeString(tags.ReplaceAllString(page, " ")))
	}
	return ""
}

func findPart(p mailPart, mime string) string {
	if p.MimeType == mime && p.Body.Data != "" {
		data, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(p.Body.Data, "="))
		if err == nil {
			return string(data)
		}
	}
	for _, sub := range p.Parts {
		if text := findPart(sub, mime); text != "" {
			return text
		}
	}
	return ""
}

// Reply is what a reply to a message needs: whom it goes to, the subject and
// the id of the message it answers, and the thread it joins.
type Reply struct {
	To        string `json:"to"`
	Subject   string `json:"subject"`
	MessageID string `json:"message_id"`
	Thread    string `json:"thread"`
}

// ReplyTo is the reply an item calls for, from the headers of its message.
func ReplyTo(item Item) (Reply, error) {
	to := item.Fields["reply_to"]
	addr, err := mail.ParseAddress(to)
	if err != nil {
		return Reply{}, fmt.Errorf("the sender %q is not an address a reply can go to", to)
	}
	return Reply{To: addr.String(), Subject: item.Title, MessageID: item.Fields["message_id"], Thread: item.ID}, nil
}

// clean takes what could end a header line out of a value taken from a
// message: it is a stranger's text.
func clean(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' || r < ' ' && r != '\t' {
			return -1
		}
		return r
	}, s)
}

// mime writes a reply as the message Gmail takes in a draft: a plain-text body
// in base64, and headers that join the thread. Every value from the message
// that is replied to is cleaned of line breaks, so no header can be added.
func (r Reply) mime(body string) (string, error) {
	addr, err := mail.ParseAddress(r.To)
	if err != nil {
		return "", fmt.Errorf("the sender %q is not an address a reply can go to", r.To)
	}
	subject := clean(r.Subject)
	if !strings.HasPrefix(strings.ToLower(subject), "re:") {
		subject = "Re: " + subject
	}
	var b strings.Builder
	b.WriteString("To: " + clean(addr.String()) + "\r\n")
	b.WriteString("Subject: " + mimeWord(subject) + "\r\n")
	if id := clean(r.MessageID); id != "" {
		b.WriteString("In-Reply-To: " + id + "\r\nReferences: " + id + "\r\n")
	}
	b.WriteString("MIME-Version: 1.0\r\nContent-Type: text/plain; charset=\"UTF-8\"\r\nContent-Transfer-Encoding: base64\r\n\r\n")
	encoded := base64.StdEncoding.EncodeToString([]byte(body))
	for len(encoded) > 76 {
		b.WriteString(encoded[:76] + "\r\n")
		encoded = encoded[76:]
	}
	b.WriteString(encoded + "\r\n")
	return b.String(), nil
}

// mimeWord encodes a header value that is not ASCII.
func mimeWord(s string) string {
	for _, r := range s {
		if r > 126 {
			return "=?UTF-8?B?" + base64.StdEncoding.EncodeToString([]byte(s)) + "?="
		}
	}
	return s
}

// SaveDraft saves body as a draft reply in the thread: a draft, never sent;
// sending is the person's, from Gmail. The credential must be bound for POST
// at Gmail's host and have the scope that creates drafts (gmail.compose),
// which is a separate authorization from the one that reads.
func (p Poller) SaveDraft(ctx context.Context, s Source, reply Reply, body string) error {
	if err := s.Validate(); err != nil {
		return err
	}
	if s.Kind != Gmail {
		return fmt.Errorf("a %s source has no mailbox to save a draft in", s.Kind)
	}
	raw, err := reply.mime(body)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(map[string]any{"message": map[string]string{"threadId": reply.Thread, "raw": base64.URLEncoding.EncodeToString([]byte(raw))}})
	if err != nil {
		return err
	}
	if _, err := p.send(ctx, s, "POST", mailBase(s)+"/gmail/v1/users/me/drafts", "header:Authorization:Bearer", payload); err != nil {
		if strings.Contains(err.Error(), "403") {
			return fmt.Errorf("%w: the credential needs the gmail.compose scope to save a draft", err)
		}
		return err
	}
	return nil
}
