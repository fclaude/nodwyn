package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestSelfcheckReplaysItsOwnSession(t *testing.T) {
	// Model another client opening a later session while the selfcheck runs.
	// This fixture tests the script's selection, not the candidate's behavior.
	candidate := filepath.Join(t.TempDir(), "candidate")
	script := `#!/bin/sh
case "$1" in
version) echo 'test candidate' ;;
eval) echo 3 ;;
--no-confirm)
  cat >/dev/null
  printf '%s\n' 'nibb session own-session' 'go-test tool' 'ok  \tgithub.com/fclaude/nibb/internal/log' 'READY'
  ;;
sessions) printf '%s\n' own-session unrelated-newer-session ;;
replay)
  test "$2" = own-session || exit 1
  echo READY
  ;;
*) exit 1 ;;
esac
`
	if err := os.WriteFile(candidate, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("bash", "../../scripts/selfcheck.sh", candidate).CombinedOutput(); err != nil {
		t.Fatalf("selfcheck chose the wrong session: %v\n%s", err, out)
	}
}
