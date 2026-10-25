package web

import (
	"encoding/json"
	"net/http"
	"strings"
)

// ManagedHTTP is a deliberately separate application protocol. It receives
// only an authenticated child/parent role, never generic daemon credentials.
// There is no RPC or linked-host fallback when this surface is selected.
type ManagedHTTP interface {
	http.Handler
	ServeChild(http.ResponseWriter, *http.Request, bool)
}

func (b *bridge) serveManaged(w http.ResponseWriter, r *http.Request, origin string) bool {
	switch r.URL.Path {
	case "/pair", "/paired", "/passkey/options", "/passkey/register", "/passkey/assert":
		return false
	case "/passkey.js", "/dom.js":
		b.asset(w, r)
		return true
	}
	if strings.HasPrefix(r.URL.Path, "/kids/api/") {
		device, ok := b.authenticated(r)
		if !ok || device.Host || (r.Method != http.MethodGet && (r.Method != http.MethodPost || r.Header.Get("Origin") != origin)) {
			http.Error(w, "Pair this browser first", http.StatusForbidden)
			return true
		}
		b.managed.ServeChild(w, r, device.Passkey != "")
		return true
	}
	if strings.HasPrefix(r.URL.Path, "/kids/admin/") {
		device, ok := b.authenticated(r)
		if !ok || device.Host || device.Passkey == "" || !b.touched(device.Hash) || r.Method != http.MethodPost || r.Header.Get("Origin") != origin {
			http.Error(w, "Confirm with the parent's passkey first", http.StatusForbidden)
			return true
		}
		var body struct {
			Name string `json:"name"`
		}
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
		d.DisallowUnknownFields()
		if err := d.Decode(&body); err != nil {
			http.Error(w, "Invalid request", http.StatusBadRequest)
			return true
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/kids/admin/devices":
			list, err := b.devices.List()
			if err != nil {
				http.Error(w, "Cannot read devices", http.StatusInternalServerError)
			} else {
				_ = json.NewEncoder(w).Encode(list)
			}
		case "/kids/admin/invite":
			code, expiry, err := b.devices.Invite(device.Name, false)
			if err != nil {
				http.Error(w, "Cannot invite a device", http.StatusInternalServerError)
			} else {
				_ = json.NewEncoder(w).Encode(map[string]any{"code": code, "expires": expiry})
			}
		case "/kids/admin/revoke":
			if err := b.devices.Revoke(body.Name); err != nil {
				http.Error(w, "Cannot revoke device", http.StatusBadRequest)
			} else {
				w.WriteHeader(http.StatusNoContent)
			}
		default:
			http.NotFound(w, r)
		}
		return true
	}
	b.managed.ServeHTTP(w, r)
	return true
}
