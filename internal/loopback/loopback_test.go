package loopback

import "testing"

func TestHost(t *testing.T) {
	for host, want := range map[string]bool{
		"localhost":      true,
		"LocalHost":      true,
		"localhost:8080": true,
		"127.0.0.1":      true,
		"127.0.0.2":      true,
		"127.0.0.2:9":    true,
		"::1":            true,
		"[::1]":          true,
		"[::1]:443":      true,
		"app.localhost":  false,
		"example.com":    false,
		"10.0.0.1":       false,
		"0.0.0.0":        false,
		"":               false,
	} {
		if got := Host(host); got != want {
			t.Errorf("Host(%q) = %v, want %v", host, got, want)
		}
	}
}
