package main

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net"
	"net/url"
)

// desktopOrigin accepts only the ephemeral loopback console created by runApp.
func desktopOrigin(address string) (string, error) {
	u, err := url.Parse(address)
	if err != nil || u.Scheme != "http" || u.User != nil || u.Port() == "" || !net.ParseIP(u.Hostname()).IsLoopback() {
		return "", errors.New("app: the desktop window requires a loopback HTTP console")
	}
	return u.Scheme + "://" + u.Host, nil
}

type desktopBridge struct {
	origin       string
	token        string
	pickFolder   func() (string, error)
	openExternal func(string) error
	notify       func() error
}

// handle accepts a bounded request after the native webview has identified its
// source frame. It exposes no session methods and never evaluates page-supplied JS.
func (b desktopBridge) handle(origin, topOrigin string, mainFrame bool, message string, reply func(string)) {
	parsed, err := desktopOrigin(origin)
	if err != nil || parsed != b.origin || (topOrigin != "" && (!mainFrame || topOrigin != b.origin)) {
		return
	}
	var request struct {
		ID     uint64 `json:"id"`
		Method string `json:"method"`
		URL    string `json:"url"`
		Token  string `json:"token"`
	}
	if len(message) > 16<<10 || json.Unmarshal([]byte(message), &request) != nil || request.ID == 0 || request.ID > 1<<53-1 {
		return
	}
	// GTK reports the top-level URI without a frame identity. The unpredictable
	// per-window capability, injected only into the top frame, covers that gap.
	if len(b.token) == 0 || subtle.ConstantTimeCompare([]byte(request.Token), []byte(b.token)) != 1 {
		return
	}
	result := struct {
		ID    uint64 `json:"id"`
		Value string `json:"value"`
		Error string `json:"error,omitempty"`
	}{ID: request.ID}
	err = nil
	switch request.Method {
	case "pickFolder":
		result.Value, err = b.pickFolder()
	case "notify":
		err = b.notify()
	case "openExternal":
		u, parseErr := url.Parse(request.URL)
		if parseErr != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
			err = errors.New("only web links open outside nodwyn")
		} else {
			err = b.openExternal(u.String())
		}
	default:
		err = errors.New("unknown desktop method")
	}
	if err != nil {
		result.Error = err.Error()
	}
	encoded, _ := json.Marshal(result)
	quotedOrigin, _ := json.Marshal(b.origin)
	// A reply after navigation must not deliver a selected path to another origin.
	reply("if(location.origin === " + string(quotedOrigin) + ") window.nodwynDesktopReply?.(" + string(encoded) + ");")
}
