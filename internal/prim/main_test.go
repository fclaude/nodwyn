package prim

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/test/fakes/home"
)

// The tests run in a home of their own (test/fakes/home): nodwyn's
// denylist, git's configuration and nodwyn's own read from it.
func TestMain(m *testing.M) {
	// original-tests runs go: it keeps the build cache and modules it has, so
	// a fresh home does not rebuild the standard library for every test.
	for _, k := range []string{"GOCACHE", "GOMODCACHE", "GOPATH"} {
		if out, err := exec.Command("go", "env", k).Output(); err == nil {
			os.Setenv(k, strings.TrimSpace(string(out)))
		}
	}
	done := home.Isolate()
	code := m.Run()
	done()
	os.Exit(code)
}
