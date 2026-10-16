package binding

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const hand = `# Hand-written: keep this note.

# GitHub, for reading and pushing.
[[bind]]
class = "github-token"
dest = "api.github.com"
positions = ["header:Authorization:Bearer"]
methods = ["GET", "POST"]

# Uploads go to another host.
[[bind]]
class = "github-token"
dest = "uploads.github.com"
positions = ["header:Authorization:Bearer"]
methods = ["PUT"]

# Another key, same host.
[[bind]]
class = "other-key"
dest = "api.github.com"
positions = ["header:Authorization:Bearer"]
methods = ["GET"]
`

func write(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bindings.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// The row a person takes back goes with all its methods, and nothing else
// changes: the comments, the other rows and the other class's row at the
// same host are as they were written.
func TestRemoveExactKeepsEveryOtherByte(t *testing.T) {
	path := write(t, hand)
	n, err := RemoveExact(context.Background(), path, "github-token", "API.github.com", []string{"header:Authorization:Bearer"}, nil)
	if err != nil || n != 1 {
		t.Fatalf("removed %d, %v", n, err)
	}
	got, _ := os.ReadFile(path)
	want := strings.Replace(hand, "[[bind]]\nclass = \"github-token\"\ndest = \"api.github.com\"\npositions = [\"header:Authorization:Bearer\"]\nmethods = [\"GET\", \"POST\"]\n", "", 1)
	if string(got) != want {
		t.Fatalf("file after:\n%s\nwant:\n%s", got, want)
	}
	table, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := table.Lookup("github-token", "api.github.com", "GET", "header:Authorization:Bearer"); ok {
		t.Fatal("the row is still bound")
	}
	if ok, _ := table.Lookup("other-key", "api.github.com", "GET", "header:Authorization:Bearer"); !ok {
		t.Fatal("another class's row went with it")
	}
	if ok, _ := table.Lookup("github-token", "uploads.github.com", "PUT", "header:Authorization:Bearer"); !ok {
		t.Fatal("another host's row went with it")
	}
}

// A row that is not there, or has other positions or scopes, is not taken:
// the file is not touched and nothing is reported removed.
func TestRemoveExactTakesOnlyTheRowNamed(t *testing.T) {
	path := write(t, hand)
	for _, c := range []struct {
		class, dest string
		positions   []string
		scopes      []string
	}{
		{"github-token", "api.github.com", []string{"query:key"}, nil},
		{"github-token", "api.github.com", []string{"header:Authorization:Bearer"}, []string{"drive"}},
		{"github-token", "elsewhere.example", []string{"header:Authorization:Bearer"}, nil},
		{"missing", "api.github.com", []string{"header:Authorization:Bearer"}, nil},
	} {
		n, err := RemoveExact(context.Background(), path, c.class, c.dest, c.positions, c.scopes)
		if err != nil || n != 0 {
			t.Fatalf("%+v: removed %d, %v", c, n, err)
		}
	}
	if got, _ := os.ReadFile(path); string(got) != hand {
		t.Fatalf("the file changed:\n%s", got)
	}
}

// A row written in a form the text cannot be cut at is refused, not guessed:
// the file stays as it was and the error says to edit it.
func TestRemoveExactRefusesWhatItCannotCut(t *testing.T) {
	inline := "bind = [{ class = \"github-token\", dest = \"api.github.com\", positions = [\"header:Authorization:Bearer\"], methods = [\"GET\"] }]\n"
	path := write(t, inline)
	n, err := RemoveExact(context.Background(), path, "github-token", "api.github.com", []string{"header:Authorization:Bearer"}, nil)
	if err == nil || n != 0 || !strings.Contains(err.Error(), "change it in") {
		t.Fatalf("removed %d, %v", n, err)
	}
	if got, _ := os.ReadFile(path); string(got) != inline {
		t.Fatalf("the file changed:\n%s", got)
	}
}

// Scopes are part of the row an OAuth2 credential was allowed with.
func TestRemoveExactMatchesScopes(t *testing.T) {
	body := "[[bind]]\nclass = \"drive\"\ndest = \"api.example.com\"\npositions = [\"header:Authorization:Bearer\"]\nmethods = [\"GET\"]\nscopes = [\"files.read\"]\n"
	path := write(t, body)
	if n, err := RemoveExact(context.Background(), path, "drive", "api.example.com", []string{"header:Authorization:Bearer"}, nil); err != nil || n != 0 {
		t.Fatalf("a row with scopes was taken without them: %d, %v", n, err)
	}
	if n, err := RemoveExact(context.Background(), path, "drive", "api.example.com", []string{"header:Authorization:Bearer"}, []string{"files.read"}); err != nil || n != 1 {
		t.Fatalf("removed %d, %v", n, err)
	}
	if got, _ := os.ReadFile(path); strings.Contains(string(got), "drive") {
		t.Fatalf("the row stayed:\n%s", got)
	}
}
