package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fclaude/nibb/internal/llm"
	"github.com/fclaude/nibb/internal/log"
	"github.com/fclaude/nibb/internal/window"
)

func TestCLIReplayWindow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	w, err := log.Create(path, "session")
	if err != nil {
		t.Fatal(err)
	}
	recipe := log.WindowContext{Version: window.RecipeVersion, System: "test system", Tools: json.RawMessage(`[]`), HighWater: 1000, LowWater: 500, MaxResultTokens: 100}
	w.Append(log.KindSessionStart, log.SessionStart{Context: &recipe})
	w.Append(log.KindUserMsg, log.UserMsg{Text: "test message"})
	events, err := log.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	built := window.Build(events, recipe.System, window.Estimate("[]"), window.Policy{HighWater: 1000, LowWater: 500, MaxResultTokens: 100})
	hash, err := window.RequestHash(llm.Request{Messages: built.Messages, Tools: []llm.Tool{}})
	if err != nil {
		t.Fatal(err)
	}
	w.Append(log.KindLLMReq, log.LLMReq{RequestHash: hash})
	w.Close()
	for _, seq := range []string{"3", "2", "0", "bad"} {
		cmd := exec.Command(os.Args[0], "-test.run=^TestCLIHelper$", "--", "replay", path, "--window", seq)
		cmd.Env = append(os.Environ(), "NIBB_TEST_CLI=1", "XDG_CONFIG_HOME="+filepath.Join(dir, "config"), "XDG_DATA_HOME="+filepath.Join(dir, "data"))
		raw, err := cmd.CombinedOutput()
		if seq != "3" {
			if err == nil {
				t.Fatal("invalid sequence succeeded", seq)
			}
			continue
		}
		if err != nil {
			t.Fatal(string(raw), err)
		}
		var req llm.Request
		if err := json.NewDecoder(strings.NewReader(string(raw))).Decode(&req); err != nil {
			t.Fatal(string(raw), err)
		}
		if len(req.Messages) != 2 || !strings.Contains(req.Messages[0].Content, "test system") || req.Messages[1].Content != "test message" {
			t.Fatal(req)
		}
	}
}
