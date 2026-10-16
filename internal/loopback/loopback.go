// Package loopback says whether a host is this machine. Where plain http is
// acceptable (a key sent to a server on this computer cannot be read on the
// way), the keychain, the configuration checks and the settings pages all
// ask the same question, and they had five answers: "127.0.0.2" was this
// machine to the keychain and not to the check of an MCP server's address.
package loopback

import (
	"net"
	"strings"
)

// Host reports whether host (a name or address, with or without a port) is
// this machine: localhost, or an address in 127.0.0.0/8 or ::1. A name that
// merely ends in .localhost is not: whether it resolves here is the
// resolver's to say.
func Host(host string) bool {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.ToLower(strings.Trim(host, "[]"))
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
