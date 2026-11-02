package rpc

import (
	"encoding/json"
	"os"
	"os/user"
	"testing"
)

func TestHelloReportsDaemonIdentity(t *testing.T) {
	path, _ := start(t, options(t))
	c := connect(t, path)
	var got map[string]any
	if err := json.Unmarshal(c.ok("hello", map[string]any{"protocol": 1}), &got); err != nil {
		t.Fatal(err)
	}
	host, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	owner, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	if got["host"] != host || got["account"] != owner.Username {
		t.Fatalf("daemon identity = %v, want %s@%s", got, owner.Username, host)
	}
}
