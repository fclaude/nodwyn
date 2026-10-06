package keychain

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
)

type insecureHTTPKey struct{}

// WithInsecureHTTP carries the user's transport policy to a credential sink.
// Only the host sets it, from user-owned configuration; Scheme cannot set it.
func WithInsecureHTTP(ctx context.Context, allow bool) context.Context {
	return context.WithValue(ctx, insecureHTTPKey{}, allow)
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
