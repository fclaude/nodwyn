// Package watch reads what a workflow's source has now (ADR 53 §4): the open
// pull requests of a Gitea or GitHub repository, or the JSON lines a
// person's command prints. It owns no timer and no log; the daemon polls
// with it and records what it saw. A forge is asked with a key from the
// keychain only where the binding table allows that key at that host for
// GET, as the guard allows http-request (SPEC, invariant 5): the daemon's
// poll is an I/O edge guarded by the mechanism that fits it.
package watch

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/fclaude/nodwyn/internal/binding"
	"github.com/fclaude/nodwyn/internal/llm"
	"github.com/fclaude/nodwyn/internal/scheme"
)

// The kinds of source.
const (
	GiteaPulls  = "gitea-pulls"
	GitHubPulls = "github-pulls"
	Command     = "command"
)

// GitHubAPI is GitHub's API, the server a GitHub source asks unless it names
// another (GitHub Enterprise, or a fake in tests).
const GitHubAPI = "https://api.github.com"

// Source is where a workflow's events come from. A forge's is its server,
// the repository (owner/name) and the keychain class of its token; a
// command's is its program and arguments, run in Dir without a shell. On is
// which changes fire: "opened" (an item not seen before) and "updated" (an
// item seen with another version: a pull request's new head commit).
type Source struct {
	Kind       string   `json:"kind"`
	Server     string   `json:"server,omitempty"`
	Repo       string   `json:"repo,omitempty"`
	Credential string   `json:"credential,omitempty"`
	Command    []string `json:"command,omitempty"`
	Dir        string   `json:"dir,omitempty"`
	On         []string `json:"on,omitempty"`
}

// Item is one thing a source has now. ID names it among the source's items,
// Version changes when it changes, and Fields are what filters read.
type Item struct {
	ID      string            `json:"id"`
	Version string            `json:"version"`
	Title   string            `json:"title"`
	URL     string            `json:"url,omitempty"`
	Fields  map[string]string `json:"fields,omitempty"`
	Body    string            `json:"body,omitempty"`
}

// Filter is one condition an item must meet to fire: Field is one of the
// item's fields (author, base, draft, labels, title, or a field a command
// prints), Op is is, is_not, contains or not_contains, compared without
// regard to case.
type Filter struct {
	Field string `json:"field"`
	Op    string `json:"op"`
	Value string `json:"value"`
}

// Event is an item that fires: Kind is "opened" or "updated".
type Event struct {
	Item Item   `json:"item"`
	Kind string `json:"kind"`
}

func (s Source) Validate() error {
	for _, on := range s.On {
		if on != "opened" && on != "updated" {
			return fmt.Errorf("a source fires on opened or updated, not %q", on)
		}
	}
	switch s.Kind {
	case GiteaPulls, GitHubPulls:
		if owner, name, ok := strings.Cut(s.Repo, "/"); !ok || owner == "" || name == "" || strings.Contains(name, "/") {
			return fmt.Errorf("the repository is owner/name, not %q", s.Repo)
		}
		if s.Credential == "" {
			return fmt.Errorf("a forge is asked with a token from the keychain: name its credential")
		}
		if s.Kind == GiteaPulls && s.Server == "" {
			return fmt.Errorf("a Gitea source names its server, https://host")
		}
		if s.Server != "" {
			u, err := url.Parse(s.Server)
			if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
				return fmt.Errorf("the server is an http or https address, not %q", s.Server)
			}
		}
	case Command:
		if len(s.Command) == 0 || strings.TrimSpace(s.Command[0]) == "" {
			return fmt.Errorf("a command source names its program")
		}
		// A program found beside the folder's files would be chosen by
		// whoever can commit to the repository (AGENTS.md: a file the host
		// reads is its committers'), so it is an absolute path or a name
		// looked up on PATH, which the person controls.
		if prog := s.Command[0]; strings.Contains(prog, "/") && !filepath.IsAbs(prog) {
			return fmt.Errorf("a command source's program is an absolute path or a name on PATH, not %q: a program in the folder would be whatever its repository holds", prog)
		}
		if s.Dir == "" {
			return fmt.Errorf("a command source runs in its workflow's folder")
		}
	default:
		return fmt.Errorf("a source is %s, %s or %s, not %q", GiteaPulls, GitHubPulls, Command, s.Kind)
	}
	return nil
}

func (f Filter) Validate() error {
	switch f.Op {
	case "is", "is_not", "contains", "not_contains":
	default:
		return fmt.Errorf("a filter is is, is_not, contains or not_contains, not %q", f.Op)
	}
	if strings.TrimSpace(f.Field) == "" {
		return fmt.Errorf("a filter names the field it reads")
	}
	return nil
}

// Match says whether item meets the filter, and when it does not, why, in
// words for Test the trigger.
func (f Filter) Match(item Item) (bool, string) {
	have := strings.ToLower(item.field(f.Field))
	want := strings.ToLower(f.Value)
	var ok bool
	switch f.Op {
	case "is":
		ok = have == want
	case "is_not":
		ok = have != want
	case "contains":
		ok = strings.Contains(have, want)
	case "not_contains":
		ok = !strings.Contains(have, want)
	}
	if ok {
		return true, ""
	}
	return false, fmt.Sprintf("%s %s %q", f.Field, strings.ReplaceAll(f.Op, "_", " "), f.Value)
}

func (i Item) field(name string) string {
	if name == "title" {
		return i.Title
	}
	return i.Fields[name]
}

// Passes says whether item meets every filter, and the first it fails.
func Passes(filters []Filter, item Item) (bool, string) {
	for _, f := range filters {
		if ok, why := f.Match(item); !ok {
			return false, why
		}
	}
	return true, ""
}

// Changes are the items that fire against what was seen (ID to version),
// in the order the source gave them.
func Changes(seen map[string]string, items []Item, on []string) []Event {
	if len(on) == 0 {
		on = []string{"opened", "updated"}
	}
	var events []Event
	for _, item := range items {
		version, known := seen[item.ID]
		switch {
		case !known && slices.Contains(on, "opened"):
			events = append(events, Event{item, "opened"})
		case known && version != item.Version && slices.Contains(on, "updated"):
			events = append(events, Event{item, "updated"})
		}
	}
	return events
}

// Keys is what a poll needs of the keychain: the key of a class, put into a
// request's header. *keychain.Store has it.
type Keys interface {
	HandleForClass(class string) *scheme.Handle
	InjectHeader(id, name, prefix string, req *http.Request) error
}

// Poller asks sources. Keys is nil while the keychain is locked; Bindings
// is the binding table as it is now; Client sends forge requests (nil: one
// that follows no redirect, so a key never goes to a host it is not bound
// to).
type Poller struct {
	Keys     Keys
	Bindings binding.Authority
	Client   *http.Client
}

var client = &http.Client{Timeout: time.Minute, CheckRedirect: llm.NoRedirect}

// ErrLocked is a forge's poll while the keychain holding its key is locked.
var ErrLocked = errors.New("the keychain is locked: unlock it, and the workflow checks again")

// maxBody bounds what one answer may be.
const maxBody = 8 << 20

// Poll reads what the source has now.
func (p Poller) Poll(ctx context.Context, s Source) ([]Item, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	switch s.Kind {
	case GiteaPulls:
		return p.pulls(ctx, s, strings.TrimRight(s.Server, "/")+"/api/v1/repos/"+s.Repo+"/pulls?state=open&sort=recentupdate&limit=50", "header:Authorization:token")
	case GitHubPulls:
		server := s.Server
		if server == "" {
			server = GitHubAPI
		}
		return p.pulls(ctx, s, strings.TrimRight(server, "/")+"/repos/"+s.Repo+"/pulls?state=open&sort=updated&direction=desc&per_page=50", "header:Authorization:Bearer")
	}
	return runCommand(ctx, s)
}

// pulls asks a forge for the open pull requests, with the source's key at
// position, if the binding table allows it there.
func (p Poller) pulls(ctx context.Context, s Source, address, position string) ([]Item, error) {
	u, err := url.Parse(address)
	if err != nil {
		return nil, err
	}
	if p.Keys == nil {
		return nil, ErrLocked
	}
	handle := p.Keys.HandleForClass(s.Credential)
	if handle == nil {
		return nil, fmt.Errorf("the keychain has no credential %q", s.Credential)
	}
	if p.Bindings == nil {
		return nil, fmt.Errorf("no binding table allows %q at %s", s.Credential, u.Hostname())
	}
	if bound, why := p.Bindings.Lookup(s.Credential, u.Hostname(), "GET", position); !bound {
		return nil, fmt.Errorf("%q may not be sent to %s for GET at %s: %s (Connections › Credentials allows a host)", s.Credential, u.Hostname(), position, why)
	}
	req, err := http.NewRequestWithContext(ctx, "GET", address, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if s.Kind == GitHubPulls {
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	}
	_, name, prefix := binding.Position(position)
	if err := p.Keys.InjectHeader(handle.ID, name, prefix, req); err != nil {
		return nil, err
	}
	c := p.Client
	if c == nil {
		c = client
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := llm.Redirected(resp, "a workflow's poll"); err != nil {
		return nil, err
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s answered %s", u.Host, resp.Status)
	}
	var pulls []struct {
		Number  int    `json:"number"`
		Title   string `json:"title"`
		Body    string `json:"body"`
		HTMLURL string `json:"html_url"`
		Draft   bool   `json:"draft"`
		User    struct {
			Login string `json:"login"`
		} `json:"user"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
		Head struct {
			SHA string `json:"sha"`
			Ref string `json:"ref"`
		} `json:"head"`
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
	}
	if err := json.Unmarshal(raw, &pulls); err != nil {
		return nil, fmt.Errorf("%s did not answer with pull requests: %w", u.Host, err)
	}
	items := make([]Item, 0, len(pulls))
	for _, pr := range pulls {
		var labels []string
		for _, l := range pr.Labels {
			labels = append(labels, l.Name)
		}
		draft := "no"
		if pr.Draft {
			draft = "yes"
		}
		items = append(items, Item{
			ID: strconv.Itoa(pr.Number), Version: pr.Head.SHA, Title: pr.Title, URL: pr.HTMLURL, Body: pr.Body,
			Fields: map[string]string{"author": pr.User.Login, "base": pr.Base.Ref, "head": pr.Head.Ref, "draft": draft, "labels": strings.Join(labels, ", ")},
		})
	}
	return items, nil
}

// commandTimeout bounds one run of a command source.
const commandTimeout = time.Minute

// runCommand runs a command source and reads its output: one JSON object
// per line, with id (required), version, title, url and body, and any other
// string fields for filters.
func runCommand(ctx context.Context, s Source) ([]Item, error) {
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.Command[0], s.Command[1:]...)
	cmd.Dir = s.Dir
	var out, errOut bytes.Buffer
	cmd.Stdout = &limited{&out, maxBody}
	cmd.Stderr = &limited{&errOut, 4096}
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s: %w: %s", s.Command[0], err, strings.TrimSpace(errOut.String()))
	}
	var items []Item
	scanner := bufio.NewScanner(&out)
	scanner.Buffer(make([]byte, 64<<10), maxBody)
	for n := 1; scanner.Scan(); n++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var fields map[string]any
		if err := json.Unmarshal([]byte(line), &fields); err != nil {
			return nil, fmt.Errorf("line %d of what %s printed is not a JSON object: %w", n, s.Command[0], err)
		}
		item := Item{Fields: map[string]string{}}
		for key, value := range fields {
			text := fmt.Sprint(value)
			switch key {
			case "id":
				item.ID = text
			case "version":
				item.Version = text
			case "title":
				item.Title = text
			case "url":
				item.URL = text
			case "body":
				item.Body = text
			default:
				item.Fields[key] = text
			}
		}
		if item.ID == "" {
			return nil, fmt.Errorf("line %d of what %s printed has no id", n, s.Command[0])
		}
		items = append(items, item)
	}
	return items, scanner.Err()
}

// limited keeps at most n bytes of what is written to it, and drops the rest.
type limited struct {
	w *bytes.Buffer
	n int
}

func (l *limited) Write(p []byte) (int, error) {
	if room := l.n - l.w.Len(); room > 0 {
		l.w.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

// Summary says what happened, in a line: "Pull request #142 opened".
func (e Event) Summary(s Source) string {
	switch s.Kind {
	case GiteaPulls, GitHubPulls:
		if e.Kind == "opened" {
			return fmt.Sprintf("Pull request #%s opened: %s", e.Item.ID, e.Item.Title)
		}
		return fmt.Sprintf("Pull request #%s got new commits: %s", e.Item.ID, e.Item.Title)
	}
	what := e.Item.Title
	if what == "" {
		what = e.Item.ID
	}
	if e.Kind == "opened" {
		return "New: " + what
	}
	return "Changed: " + what
}

// Text is the event as a run reads it, the content of the file it is
// attached as: what happened, the item's fields and its text.
func (e Event) Text(s Source) string {
	var b strings.Builder
	b.WriteString("# " + e.Summary(s) + "\n\n")
	switch s.Kind {
	case GiteaPulls, GitHubPulls:
		server := s.Server
		if server == "" {
			server = GitHubAPI
		}
		fmt.Fprintf(&b, "- Repository: %s (%s)\n- Head: %s\n", s.Repo, server, e.Item.Version)
	default:
		fmt.Fprintf(&b, "- From: %s\n- Id: %s\n", strings.Join(s.Command, " "), e.Item.ID)
	}
	if e.Item.URL != "" {
		fmt.Fprintf(&b, "- Address: %s\n", e.Item.URL)
	}
	keys := make([]string, 0, len(e.Item.Fields))
	for key := range e.Item.Fields {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		if e.Item.Fields[key] != "" {
			fmt.Fprintf(&b, "- %s: %s\n", strings.ToUpper(key[:1])+key[1:], e.Item.Fields[key])
		}
	}
	if body := strings.TrimSpace(e.Item.Body); body != "" {
		b.WriteString("\n" + body + "\n")
	}
	return b.String()
}
