package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestSelfcheckReplaysItsOwnSession(t *testing.T) {
	// Model another client opening a later session while the selfcheck runs.
	// This fixture tests the script's selection and its trust of the repository,
	// not the candidate's behavior.
	candidate := filepath.Join(t.TempDir(), "candidate")
	script := `#!/bin/sh
case "$1" in
version) echo 'test candidate' ;;
eval) echo 3 ;;
trust)
  case "$4 $5" in
  ' ') echo 'Revision: rev1' ;;
  '--accept rev1') touch "$XDG_CONFIG_HOME/trusted" ;;
  *) exit 1 ;;
  esac
  ;;
--no-confirm)
  cat >/dev/null
  echo 'nodwyn session own-session'
  # Like the real candidate, the dev tools of an untrusted repository are skipped.
  if test -e "$XDG_CONFIG_HOME/trusted"; then
    printf '%s\n' 'go-test tool' 'ok  \tgithub.com/fclaude/nodwyn/internal/log'
  fi
  echo READY
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
