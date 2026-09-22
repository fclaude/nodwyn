package redact

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestJSONEscapesAndNumbers(t *testing.T) {
	raw := []byte(`{"args":{"text":"\u0073k-ant-abcdefgh1234","pem":"-----BEGIN PRIVATE KEY-----\nYWJjZA==\n-----END PRIVATE KEY-----"},"n":9007199254740993}`)
	out, events, err := JSON(raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || !json.Valid(out) || strings.Contains(string(out), "abcdefgh") || !strings.Contains(string(out), "9007199254740993") {
		t.Fatalf("bad redaction: %s %+v", out, events)
	}
	again, events, err := JSON(out, nil)
	if err != nil || len(events) != 0 || string(again) != string(out) {
		t.Fatal("redaction is not idempotent")
	}
}

func TestJSONKeyCollisionFailsClosed(t *testing.T) {
	_, _, err := JSON([]byte(`{"sk-ant-abcdefgh1234":1,"sk-ant-abcdefgh5678":2}`), nil)
	if err == nil {
		t.Fatal("silently discarded a key")
	}
}

func TestJSONEncodedToolOutput(t *testing.T) {
	inner, _ := json.Marshal(map[string]string{"stdout": "-----BEGIN PRIVATE KEY-----\nYWJjZA==\n-----END PRIVATE KEY-----"})
	raw, _ := json.Marshal(map[string]string{"value": string(inner)})
	out, events, err := JSON(raw, nil)
	if err != nil || len(events) != 1 || strings.Contains(string(out), "YWJjZA") {
		t.Fatalf("encoded tool output: %s %v", out, err)
	}
}
