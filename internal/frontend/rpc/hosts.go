package rpc

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
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/coder/websocket"

	"github.com/fclaude/nodwyn/internal/binding"
	"github.com/fclaude/nodwyn/internal/core"
	"github.com/fclaude/nodwyn/internal/keychain"
	"github.com/fclaude/nodwyn/internal/policyfile"
)

// Hosts are the other nodwyns this computer is linked to (ADR 54). A link is
// a device of the other host's console: this daemon paired with it by an
// invitation made there, keeps the device's cookie in the keychain, and
// relays a console's socket to it, so one window shows every host's tasks.
// The other host's rules decide what the link may do there.
//
// hosts.toml in the config directory says, for each link, its name, the
// host's address and the name this computer has there. This daemon writes
// it, as the user, in the config directory (0700), which a task's file tools
// never reach. It holds nothing secret, and the one thing it decides, the
// address dialled, is checked against the binding table, so an address
// changed there is sent no cookie.

// hostEntry is one link in hosts.toml.
type hostEntry struct {
	Name    string    `toml:"name" json:"name"`
	Address string    `toml:"address" json:"address"`
	As      string    `toml:"as" json:"as"`
	Added   time.Time `toml:"added" json:"added"`
}

type hostFile struct {
	Host []hostEntry `toml:"host"`
}

// hostInfo is a link as host.list reports it.
type hostInfo struct {
	hostEntry
	// Locked says why the link cannot be used now, when it cannot: its
	// token is in a keychain that is locked, or is not there at all.
	Locked string `json:"locked,omitempty"`
}

type hostLinkParams struct {
	Name    string `json:"name"`    // what this computer calls the host, such as workshop
	Address string `json:"address"` // the host's console, such as https://workshop.example.net
	As      string `json:"as"`      // the name this computer has there, such as laptop
	Invite  string `json:"invite"`  // an invitation made there
}

type hostNameParams struct {
	Name string `json:"name"`
}

// hostUnlinkResult says whether the host was told to forget this computer.
type hostUnlinkResult struct {
	Told   bool   `json:"told"`
	Detail string `json:"detail,omitempty"`
}

type hostAttachParams struct {
	Name string `json:"name"` // the host
	ID   string `json:"id"`   // the task there
	Path string `json:"path"` // the file, staged on this computer by the console
	File string `json:"file"` // the file's name, as the person chose it
}

// hostClosed is the notification that ends a relay the host closed: the
// code and reason of its close.
type hostClosed struct {
	Code   int    `json:"code"`
	Reason string `json:"reason"`
}

// The failures of host.connect a console tells apart.
const (
	hostRefusedCode     = -32040 // the host does not take this link: link it again
	hostUnreachableCode = -32041 // the host cannot be reached now
	hostLockedCode      = -32042 // the link's token cannot be sent: the keychain is locked, or it is gone
)

// linkName is what a host is called here and what this computer is called
// there: a device's name (web.DeviceName).
var linkName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,31}$`)

// linkClass is the keychain class of a link's token.
func linkClass(name string) string { return "nodwyn-host-" + name }

// hostPosition is where a link's token goes: it is the cookie the host's
// console set, kept with its name (nodwyn_device=…).
const hostPosition = "header:Cookie"

// hostCookie is the name of the cookie a console keeps a device's token in.
const hostCookie = "nodwyn_device"

func (s *server) hostsPath() string { return filepath.Join(s.options.Paths.ConfigDir, "hosts.toml") }

func (s *server) readHosts() ([]hostEntry, error) {
	var f hostFile
	_, err := toml.DecodeFile(s.hostsPath(), &f)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return f.Host, err
}

// changeHosts rewrites hosts.toml under its lock with what change makes of
// the links.
func (s *server) changeHosts(change func([]hostEntry) ([]hostEntry, error)) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return policyfile.Update(ctx, s.hostsPath(), func(raw []byte) ([]byte, error) {
		var f hostFile
		if _, err := toml.Decode(string(raw), &f); err != nil {
			return nil, err
		}
		hosts, err := change(f.Host)
		if err != nil {
			return nil, err
		}
		var out bytes.Buffer
		out.WriteString("# Other nodwyns this computer is linked to (ADR 54). Their tokens are in the keychain.\n")
		if err := toml.NewEncoder(&out).Encode(hostFile{hosts}); err != nil {
			return nil, err
		}
		return out.Bytes(), nil
	})
}

// host finds the link named name.
func (s *server) host(name string) (hostEntry, *rpcError) {
	hosts, err := s.readHosts()
	if err != nil {
		return hostEntry{}, failure(-32000, err.Error())
	}
	for _, h := range hosts {
		if h.Name == name {
			return h, nil
		}
	}
	return hostEntry{}, failure(-32602, fmt.Sprintf("no host is linked as %q", name))
}

// hostLocked says why a link's token cannot be sent now, or "".
func (s *server) hostLocked(name string) string {
	switch {
	case s.options.Keychain == nil:
		return "the keychain is locked: unlock it to reach this host"
	case s.options.Keychain.HandleForClass(linkClass(name)) == nil:
		return "its token is not in the keychain: link it again"
	}
	return ""
}

// hostOrigin is the origin of a host's console at address (scheme://host:port).
func hostOrigin(address string) (*url.URL, error) {
	u, err := url.Parse(address)
	switch {
	case err != nil:
		return nil, errors.New("the address is not one: give the host's console, such as https://workshop.example.net")
	case u.Scheme != "https" && u.Scheme != "http":
		return nil, errors.New("the address starts with https:// (or http:// for this computer or a network you trust)")
	case u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "":
		return nil, errors.New("give the host's console alone, such as https://workshop.example.net, with no path")
	}
	return &url.URL{Scheme: u.Scheme, Host: strings.ToLower(u.Host)}, nil
}

func (s *server) hostMethod(req request, client *peer) (any, *rpcError) {
	// A link is this computer's authority on another host: a paired device
	// that only answers here neither uses one nor makes one (ADR 54).
	if why := client.refuseDevice("Other hosts are linked and reached from this computer, or from a device signed in with your passkey"); why != nil {
		return nil, why
	}
	switch req.Method {
	case "host.list":
		hosts, err := s.readHosts()
		if err != nil {
			return nil, failure(-32000, err.Error())
		}
		out := []hostInfo{}
		for _, h := range hosts {
			out = append(out, hostInfo{h, s.hostLocked(h.Name)})
		}
		return out, nil
	case "host.link":
		return s.hostLink(req)
	case "host.unlink":
		return s.hostUnlink(req)
	case "host.connect":
		return s.hostConnect(req, client)
	case "host.attach":
		return s.hostAttach(req)
	}
	return nil, failure(-32601, "Method not found")
}

// hostLink answers host.link: it pairs this computer with the host's console
// as a device named as, with an invitation made there, and keeps the cookie
// it sets in the keychain, bound to the host's name.
func (s *server) hostLink(req request) (any, *rpcError) {
	var p hostLinkParams
	if err := params(req.Params, &p, "name", "address", "as", "invite"); err != nil {
		return nil, err
	}
	switch {
	case !linkName.MatchString(p.Name):
		return nil, failure(-32602, "name the host with lower-case letters, digits, dots or dashes (at most 32), such as workshop")
	case !linkName.MatchString(p.As):
		return nil, failure(-32602, "name this computer with lower-case letters, digits, dots or dashes (at most 32), such as laptop")
	case strings.TrimSpace(p.Invite) == "":
		return nil, failure(-32602, "enter the invitation made on the host")
	}
	at, err := hostOrigin(p.Address)
	if err != nil {
		return nil, failure(-32602, err.Error())
	}
	if _, why := s.host(p.Name); why == nil {
		return nil, failure(-32602, fmt.Sprintf("a host is linked as %s already: unlink it first, or choose another name", p.Name))
	}
	store := s.options.Keychain
	if store == nil {
		return nil, failure(-32000, "the link's token is kept in the keychain, which is locked or not made yet: unlock or create it first")
	}
	class := linkClass(p.Name)
	if store.HandleForClass(class) != nil {
		return nil, failure(-32000, fmt.Sprintf("the keychain holds a credential of class %s already: remove it in Connections first", class))
	}
	var cookie string
	s.slow("", func() { cookie, err = pairWith(s.ctx, at, p.Invite, p.As, s.options.Config.Policy.AllowInsecureHTTP) })
	if err != nil {
		return nil, failure(-32000, err.Error())
	}
	rows := []binding.Binding{}
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		row, err := binding.Exact(class, at.Hostname(), method, hostPosition)
		if err != nil {
			return nil, failure(-32000, err.Error())
		}
		rows = append(rows, row)
	}
	s.slow("", func() { _, err = store.Add(class, []byte(cookie)) })
	if err != nil {
		return nil, failure(-32000, "the host paired this computer, but its token could not be kept: "+err.Error()+"; revoke "+p.As+" there and link again")
	}
	if err := s.appendRows(rows); err != nil {
		return nil, failure(-32000, err.Error())
	}
	entry := hostEntry{Name: p.Name, Address: at.String(), As: p.As, Added: time.Now().UTC().Truncate(time.Second)}
	if err := s.changeHosts(func(hosts []hostEntry) ([]hostEntry, error) { return append(hosts, entry), nil }); err != nil {
		return nil, failure(-32000, err.Error())
	}
	return hostInfo{entry, s.hostLocked(p.Name)}, nil
}

// noRedirect keeps a request to a host where it was sent: a console never
// redirects, and the invitation or the cookie would go along to the next.
func noRedirect(*http.Request, []*http.Request) error {
	return errors.New("the host redirected the request")
}

// pairWith posts invite to the console at at, naming this computer as, and
// returns the cookie it sets. The invitation is a secret, so it is sent as a
// credential is: over https, or to this computer, unless the person allowed
// plain http to other hosts.
func pairWith(ctx context.Context, at *url.URL, invite, as string, insecure bool) (string, error) {
	body, _ := json.Marshal(map[string]any{"invite": strings.TrimSpace(invite), "device": as, "host": true})
	ctx, cancel := context.WithTimeout(keychain.WithInsecureHTTP(ctx, insecure), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, at.String()+"/pair", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	if err := keychain.CheckTransport(req); err != nil {
		return "", errors.New("an invitation is sent over https, or to this computer: " + err.Error())
	}
	req.Header.Set("Origin", at.String())
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{CheckRedirect: noRedirect}).Do(req)
	if err != nil {
		return "", fmt.Errorf("could not reach %s: %w", at.Host, keychain.SendError(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return "", fmt.Errorf("%s answered: %s", at.Host, hostAnswer(resp))
	}
	for _, c := range resp.Cookies() {
		if c.Name == hostCookie && c.Value != "" {
			return hostCookie + "=" + c.Value, nil
		}
	}
	return "", fmt.Errorf("%s paired this computer but set no device cookie: is it nodwyn?", at.Host)
}

// hostAnswer is what a host said in a response that is not the one asked for,
// on one line and short, since it is shown to the person.
func hostAnswer(resp *http.Response) string {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	text := strings.Join(strings.Fields(string(raw)), " ")
	if text == "" {
		text = resp.Status
	}
	return text
}

// authorizing puts a link's cookie on each request through the authorizer a
// model's key goes through (core.Keyed): only to the host the binding names,
// and only over https or to this computer.
type authorizing struct {
	base      http.RoundTripper
	authorize func(*http.Request) error
}

func (a authorizing) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	if err := a.authorize(req); err != nil {
		return nil, unauthorized{err}
	}
	return a.base.RoundTrip(req)
}

// unauthorized is the authorizer's refusal to send a link's cookie: the
// keychain is locked, the token is gone, or the binding does not allow it.
type unauthorized struct{ error }

func (u unauthorized) Unwrap() error { return u.error }

// hostClient is the client a link's requests go through.
func hostClient(name string, keyed core.Keyed) *http.Client {
	return &http.Client{Transport: authorizing{http.DefaultTransport, keyed.Authorize(linkClass(name), hostPosition)}, CheckRedirect: noRedirect}
}

// linkError is a failure to reach a host, with the code host.connect gives
// it.
type linkError struct {
	code    int
	message string
}

func (e *linkError) Error() string { return e.message }

// dialHost opens the host's console socket as this computer's device. The
// connection lives until ctx ends or it is closed; a host that has not
// answered within 15 seconds is given up on.
func dialHost(ctx context.Context, h hostEntry, keyed core.Keyed) (*websocket.Conn, error) {
	at, err := hostOrigin(h.Address)
	if err != nil {
		return nil, &linkError{hostRefusedCode, err.Error()}
	}
	socket := *at
	socket.Path = "/socket"
	dialCtx, cancel := context.WithCancel(ctx)
	timer := time.AfterFunc(15*time.Second, cancel)
	defer timer.Stop()
	ws, resp, err := websocket.Dial(dialCtx, socket.String(), &websocket.DialOptions{HTTPClient: hostClient(h.Name, keyed), HTTPHeader: http.Header{"Origin": {at.String()}}})
	if err != nil {
		cancel()
		var refused unauthorized
		switch {
		case resp != nil && resp.StatusCode == http.StatusForbidden:
			return nil, &linkError{hostRefusedCode, fmt.Sprintf("%s does not take this computer any more: it was revoked there, or not used for 90 days; link it again", h.Name)}
		case errors.As(err, &refused):
			return nil, &linkError{hostLockedCode, fmt.Sprintf("%s cannot be reached with this computer's token: %s", h.Name, rootCause(err))}
		case resp != nil:
			return nil, &linkError{hostUnreachableCode, fmt.Sprintf("%s answered %s", h.Name, resp.Status)}
		}
		return nil, &linkError{hostUnreachableCode, fmt.Sprintf("%s cannot be reached: %s", h.Name, rootCause(err))}
	}
	ws.SetReadLimit(maxRelayed)
	// The handshake's context is the connection's: it ends with the relay.
	context.AfterFunc(ctx, cancel)
	return ws, nil
}

// rootCause is the innermost error of err, without the wrappers that repeat
// the address and the library's names.
func rootCause(err error) string {
	for {
		next := errors.Unwrap(err)
		if next == nil {
			return keychain.SendError(err).Error()
		}
		err = next
	}
}

// maxRelayed bounds a message a host sends a console: a page of a long log
// can be larger than a request may be.
const maxRelayed = 16 << 20

// hostConnect answers host.connect: it dials the host, and after its reply
// the connection relays the console's messages to it and its messages back.
func (s *server) hostConnect(req request, client *peer) (any, *rpcError) {
	var p hostNameParams
	if err := params(req.Params, &p, "name"); err != nil {
		return nil, err
	}
	h, why := s.host(p.Name)
	if why != nil {
		return nil, why
	}
	if locked := s.hostLocked(p.Name); locked != "" {
		return nil, failure(hostLockedCode, h.Name+": "+locked)
	}
	keyed := s.keyed()
	var ws *websocket.Conn
	var err error
	s.slow("", func() { ws, err = dialHost(s.ctx, h, keyed) })
	if err != nil {
		var le *linkError
		if errors.As(err, &le) {
			return nil, failure(le.code, le.message)
		}
		return nil, failure(hostUnreachableCode, err.Error())
	}
	client.relay = func(ctx context.Context, lines *bufio.Scanner) { relayLink(ctx, ws, lines, client) }
	return struct{}{}, nil
}

// relayLink carries a link's messages until either end closes: each line
// the console's bridge sends goes to the host as one message, and each of
// the host's messages comes back as one line. When the host closes, the
// bridge is told why (host.closed) before the connection ends.
func relayLink(ctx context.Context, ws *websocket.Conn, lines *bufio.Scanner, client *peer) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer ws.CloseNow()
	go func() {
		defer cancel()
		for lines.Scan() {
			if ws.Write(ctx, websocket.MessageText, lines.Bytes()) != nil {
				return
			}
		}
		// The bridge closed: so does the link.
		ws.Close(websocket.StatusNormalClosure, "")
	}()
	for {
		_, message, err := ws.Read(ctx)
		if err != nil {
			if ctx.Err() == nil {
				closed := hostClosed{Code: int(websocket.StatusGoingAway), Reason: "the host closed the connection"}
				var ce websocket.CloseError
				if errors.As(err, &ce) {
					closed = hostClosed{int(ce.Code), ce.Reason}
				}
				client.enqueue(notification{"2.0", "host.closed", closed})
				client.flush(5 * time.Second)
			}
			return
		}
		if !json.Valid(message) || !client.enqueue(json.RawMessage(message)) {
			return
		}
	}
}

// hostUnlink answers host.unlink: it asks the host to forget this computer,
// then forgets the link's token, its binding rows and its line in
// hosts.toml.
func (s *server) hostUnlink(req request) (any, *rpcError) {
	var p hostNameParams
	if err := params(req.Params, &p, "name"); err != nil {
		return nil, err
	}
	h, why := s.host(p.Name)
	if why != nil {
		return nil, why
	}
	store := s.options.Keychain
	if store == nil {
		return nil, failure(-32000, "the link's token is in the keychain, which is locked: unlock it to unlink "+h.Name)
	}
	result := hostUnlinkResult{}
	if s.hostLocked(h.Name) == "" {
		keyed := s.keyed()
		var err error
		s.slow("", func() { err = leaveHost(s.ctx, h, keyed) })
		if err != nil {
			result.Detail = fmt.Sprintf("%s could not be told (%s): revoke %s in its Settings › Keys & devices", h.Name, err.Error(), h.As)
		} else {
			result.Told = true
		}
	}
	if handle := store.HandleForClass(linkClass(h.Name)); handle != nil {
		var err error
		s.slow("", func() { err = store.Delete(handle.ID) })
		if err != nil {
			return nil, failure(-32000, err.Error())
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := binding.RemoveClass(ctx, s.options.Paths.Bindings, linkClass(h.Name)); err != nil {
		return nil, failure(-32000, err.Error())
	}
	if err := s.reloadBindings(); err != nil {
		return nil, failure(-32000, err.Error())
	}
	if err := s.changeHosts(func(hosts []hostEntry) ([]hostEntry, error) {
		return slices.DeleteFunc(hosts, func(x hostEntry) bool { return x.Name == h.Name }), nil
	}); err != nil {
		return nil, failure(-32000, err.Error())
	}
	return result, nil
}

// leaveHost asks the host to forget this computer (devices.leave), over a
// connection of its own.
func leaveHost(ctx context.Context, h hostEntry, keyed core.Keyed) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	ws, err := dialHost(ctx, h, keyed)
	if err != nil {
		return err
	}
	defer ws.CloseNow()
	if err := ws.Write(ctx, websocket.MessageText, []byte(`{"jsonrpc":"2.0","id":1,"method":"devices.leave","params":{}}`)); err != nil {
		return err
	}
	for {
		_, message, err := ws.Read(ctx)
		if err != nil {
			return err
		}
		var reply struct {
			ID    uint64 `json:"id"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(message, &reply) != nil || reply.ID != 1 {
			continue // a notification
		}
		if reply.Error != nil {
			return errors.New(reply.Error.Message)
		}
		ws.Close(websocket.StatusNormalClosure, "")
		return nil
	}
}

// maxAttached bounds a file sent to a host's task: what its console takes
// (web.maxUpload).
const maxAttached = 20 << 20

// hostAttach answers host.attach: it sends a file the console staged on this
// computer to a task on the host, through the host's console, which attaches
// it there as its own console would, and returns what that answered.
func (s *server) hostAttach(req request) (any, *rpcError) {
	var p hostAttachParams
	if err := params(req.Params, &p, "name", "id", "path", "file"); err != nil {
		return nil, err
	}
	h, why := s.host(p.Name)
	if why != nil {
		return nil, why
	}
	if locked := s.hostLocked(h.Name); locked != "" {
		return nil, failure(hostLockedCode, h.Name+": "+locked)
	}
	at, err := hostOrigin(h.Address)
	if err != nil {
		return nil, failure(-32000, err.Error())
	}
	file, err := os.Open(p.Path)
	if err != nil {
		return nil, failure(-32000, "the file to send is gone: "+err.Error())
	}
	defer file.Close()
	if info, err := file.Stat(); err != nil || !info.Mode().IsRegular() || info.Size() > maxAttached {
		return nil, failure(-32602, fmt.Sprintf("a file sent to another host is a regular file of at most %d MB", maxAttached>>20))
	}
	keyed := s.keyed()
	var receipt json.RawMessage
	var sendErr error
	s.slow("", func() {
		// An image waits for its confirm on the host, which gives up after
		// ten minutes; the request waits a little longer than that.
		ctx, cancel := context.WithTimeout(s.ctx, 11*time.Minute)
		defer cancel()
		address := at.String() + "/attach?" + url.Values{"id": {p.ID}, "name": {p.File}}.Encode()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, address, file)
		if err != nil {
			sendErr = err
			return
		}
		req.Header.Set("Origin", at.String())
		req.Header.Set("Content-Type", "application/octet-stream")
		resp, err := hostClient(h.Name, keyed).Do(req)
		if err != nil {
			sendErr = fmt.Errorf("could not send the file to %s: %s", h.Name, rootCause(err))
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			sendErr = errors.New(hostAnswer(resp))
			return
		}
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if err != nil || !json.Valid(raw) {
			sendErr = fmt.Errorf("%s's answer to the file was not understood", h.Name)
			return
		}
		receipt = raw
	})
	if sendErr != nil {
		return nil, failure(-32000, sendErr.Error())
	}
	return receipt, nil
}
