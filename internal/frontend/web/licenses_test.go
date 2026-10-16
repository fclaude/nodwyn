package web

import (
	"os"
	"strings"
	"testing"
)

// Every notice the binary prints exists: a file name mistyped in notices
// printed nothing, since the read error was dropped.
func TestEveryNoticeIsEmbedded(t *testing.T) {
	for _, n := range notices {
		text, err := licenseFiles.ReadFile("licenses/" + n.file)
		if err != nil || len(strings.TrimSpace(string(text))) == 0 {
			t.Errorf("%s: licenses/%s is missing or empty: %v", n.name, n.file, err)
		}
	}
	if !strings.Contains(Licenses(), "Go library: github.com/coder/websocket ") {
		t.Error("nodwyn licenses does not name the websocket library")
	}
}

// Every Go module nodwyn requires, but the ones only its build tools use, has
// a notice in the binary (P2-088): a dependency added to go.mod fails this
// until python3 scripts/licenses.py writes its licence.
func TestEveryModuleHasANotice(t *testing.T) {
	mod, err := os.ReadFile("../../../go.mod")
	if err != nil {
		t.Fatal(err)
	}
	// Used by `go tool staticcheck` and linked into nothing nodwyn ships.
	toolOnly := map[string]bool{"honnef.co/go/tools": true, "golang.org/x/exp/typeparams": true, "golang.org/x/mod": true, "golang.org/x/tools": true}
	notice, err := licenseFiles.ReadFile("licenses/go-modules.txt")
	if err != nil {
		t.Fatal(err)
	}
	inRequire := false
	for _, line := range strings.Split(string(mod), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "require (":
			inRequire = true
		case line == ")":
			inRequire = false
		case inRequire && line != "" && !strings.HasPrefix(line, "//"):
			path := strings.Fields(line)[0]
			if !toolOnly[path] && !strings.Contains(string(notice), "Go library: "+path+" ") {
				t.Errorf("%s is required by go.mod but has no licence in go-modules.txt: run python3 scripts/licenses.py", path)
			}
		}
	}
}
