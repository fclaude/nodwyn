package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fclaude/nibb/internal/config"
	"github.com/fclaude/nibb/internal/log"
	"github.com/fclaude/nibb/internal/window"
)

func TestCLISummaryLifecycle(t *testing.T) {
	dir := t.TempDir()
	paths := config.PathsUnder(filepath.Join(dir, "config", "nibb"), filepath.Join(dir, "data", "nibb"))
	id := "summary-test"
	writer, err := log.Create(log.SessionPath(paths.SessionsDir, id), id)
	if err != nil {
		t.Fatal(err)
	}
	writer.Append(log.KindSessionStart, log.SessionStart{Repo: t.TempDir()})
	for range 3 {
		writer.Append(log.KindUserMsg, log.UserMsg{Text: "question"})
		writer.Append(log.KindLLMResp, log.LLMResp{Content: "answer"})
	}
	writer.Close()
	text := filepath.Join(dir, "summary.txt")
	if err := os.WriteFile(text, []byte("Earlier work is complete."), 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(success bool, args ...string) string {
		t.Helper()
		cmd := exec.Command(os.Args[0], append([]string{"-test.run=^TestCLIHelper$", "--", "summary"}, args...)...)
		cmd.Env = append(os.Environ(), "NIBB_TEST_CLI=1", "XDG_CONFIG_HOME="+filepath.Join(dir, "config"), "XDG_DATA_HOME="+filepath.Join(dir, "data"))
		raw, err := cmd.CombinedOutput()
		if (err == nil) != success {
			t.Fatalf("args=%v err=%v output=%s", args, err, raw)
		}
		return string(raw)
	}
	run(false, "--drop", "0", id)
	run(false, "--drop", "8", "--from", "0", id)
	run(false, "--from", "0", id)
	run(false, "--from", "2", id)
	run(true, "--from", "2", "--through", "3", "--text", text, id)
	var views []window.SummaryInfo
	if err := json.NewDecoder(strings.NewReader(run(true, id))).Decode(&views); err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 || views[0].Status != "active" || views[0].Seq != 8 {
		t.Fatal(views)
	}
	run(true, "--drop", "8", id)
	if err := json.NewDecoder(strings.NewReader(run(true, id))).Decode(&views); err != nil {
		t.Fatal(err)
	}
	if views[0].Status != "dropped" {
		t.Fatal(views)
	}
	run(false, "--drop", "8", id)
	run(false, "--drop", "8", "--text", text, id)
}
