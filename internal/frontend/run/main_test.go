package run

import (
	"os"
	"testing"

	"github.com/fclaude/nodwyn/test/fakes/home"
)

// The tests run in a home of their own (test/fakes/home): nodwyn's
// denylist, git's configuration and nodwyn's own read from it.
func TestMain(m *testing.M) {
	done := home.Isolate()
	code := m.Run()
	done()
	os.Exit(code)
}
