package keychain

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
)

type insecureHTTPKey struct{}

// WithInsecureHTTP carries the user's transport policy to a credential sink.
// Only the host sets it, from user-owned configuration; Scheme cannot set it.
func WithInsecureHTTP(ctx context.Context, allow bool) context.Context {
	return context.WithValue(ctx, insecureHTTPKey{}, allow)
}

// SendError is the error to report when a keyed request could not be sent.
// net/http names the whole address in a *url.Error ("Post \"https://h/p?key=…\":
// dial tcp: connection refused"), so a key InjectQuery put in the query would
// go along into a tool's result, the log and the model's context. The address
// keeps its scheme, host and path, which say where the request went.
func SendError(err error) error {
	var ue *url.Error
	if err == nil || !errors.As(err, &ue) {
		return err
	}
	u, perr := url.Parse(ue.URL)
	if perr != nil {
		return &url.Error{Op: ue.Op, URL: "(address withheld)", Err: ue.Err}
	}
	u.RawQuery, u.ForceQuery, u.Fragment, u.RawFragment, u.User = "", false, "", "", nil
	return &url.Error{Op: ue.Op, URL: u.String(), Err: ue.Err}
}

// CheckTransport refuses to put a credential on an unencrypted remote link
// unless the user enabled that transport. Local development servers remain usable.
func CheckTransport(req *http.Request) error {
	if req == nil || req.URL == nil || req.URL.Hostname() == "" || req.URL.User != nil {
		return errors.New("keychain: credential request requires a host and no URL userinfo")
	}
	if req.URL.Scheme == "https" {
		return nil
	}
	if req.URL.Scheme == "http" {
		host := req.URL.Hostname()
		ip := net.ParseIP(host)
		allow, _ := req.Context().Value(insecureHTTPKey{}).(bool)
		if allow || strings.EqualFold(host, "localhost") || (ip != nil && ip.IsLoopback()) {
			return nil
		}
	}
	return errors.New("keychain: credentials require HTTPS; to allow remote HTTP, enable policy.allow_insecure_http in the user's settings")
}
