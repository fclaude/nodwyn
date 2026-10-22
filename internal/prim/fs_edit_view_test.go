package prim

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fclaude/brote/scheme"
)

// editView is a harness with fixture.go holding text and every confirm
// answered yes, so what each edit does is what the test sees.
type editView struct {
	*harness
	file string
}

func newEditView(t *testing.T, text string) editView {
	h := newHarness(t)
	h.answer = true
	file := filepath.Join(h.root, "fixture.go")
	if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return editView{h, file}
}

func (e editView) text(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(e.file)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func q(s string) string { return scheme.Write(scheme.Str(s)) }

// edit calls fs-edit as the model would, and reports the last confirm.
func (e editView) edit(t *testing.T, old, new string, all bool) error {
	t.Helper()
	call := `(fs-edit "fixture.go" ` + q(old) + ` ` + q(new)
	if all {
		call += ` #t`
	}
	_, err := e.eval(t, call+`)`)
	return err
}

// The credential-shaped texts the tests use are built apart, so that this file
// holds none: what ADR 43 is about is that the model cannot read one.
const (
	anthropic = "sk-ant-" + "abcdefgh1234"
	aws       = "AKIA" + "0123456789ABCDEF"
	holder    = "[REDACTED anthropic-key]"
	awsHolder = "[REDACTED aws-access-key]"
)

// A line with a credential on it is edited through its placeholder: the
// credential stays where it was and never reaches the model.
func TestFsEditKeepsWhatAPlaceholderStandsFor(t *testing.T) {
	e := newEditView(t, "name := \"credential\"\nbody := \""+anthropic+"\"\ntail := 1\n")
	if err := e.edit(t, `body := "`+holder+`"`, `body := "`+holder+`" // fixture`, false); err != nil {
		t.Fatal(err)
	}
	if got, want := e.text(t), "name := \"credential\"\nbody := \""+anthropic+"\" // fixture\ntail := 1\n"; got != want {
		t.Fatalf("file:\n%s\nwant:\n%s", got, want)
	}
	// The diff the person is shown has what the file holds.
	if d := e.prompts[len(e.prompts)-1].Detail; !strings.Contains(d, anthropic) {
		t.Fatalf("the preview does not show the real change:\n%s", d)
	}

	// Leaving the placeholder out of new drops the credential.
	if err := e.edit(t, `body := "`+holder+`" // fixture`, `body := ""`, false); err != nil {
		t.Fatal(err)
	}
	if got, want := e.text(t), "name := \"credential\"\nbody := \"\"\ntail := 1\n"; got != want {
		t.Fatalf("dropped: %q", got)
	}
}

// Replacing a credential with another one the model writes writes the text as
// it is given.
func TestFsEditReplacesACredentialWithAnother(t *testing.T) {
	e := newEditView(t, "body := \""+anthropic+"\"\n")
	if err := e.edit(t, `body := "`+holder+`"`, `body := "`+"sk-ant-"+`zzzzzzzz9999"`, false); err != nil {
		t.Fatal(err)
	}
	if got, want := e.text(t), "body := \"sk-ant-zzzzzzzz9999\"\n"; got != want {
		t.Fatalf("file: %q", got)
	}
}

// What an edit cannot do is move a credential: new has the placeholders old
// has, in order, or none, so none is copied, reordered or half kept.
func TestFsEditPairsPlaceholdersOrRefuses(t *testing.T) {
	text := "a := \"" + anthropic + "\"\nb := \"" + aws + "\"\n"
	e := newEditView(t, text)
	old := "a := \"" + holder + "\"\nb := \"" + awsHolder + "\""
	for name, new := range map[string]string{
		"copied":    old + "\nc := \"" + holder + "\"",
		"reordered": "b := \"" + awsHolder + "\"\na := \"" + holder + "\"",
		"half kept": "a := \"" + holder + "\"\nb := 1",
		"swapped":   "a := \"" + awsHolder + "\"\nb := \"" + holder + "\"",
	} {
		err := e.edit(t, old, new, false)
		if err == nil || !strings.Contains(err.Error(), "same placeholders as old, in the same order") {
			t.Fatalf("%s: %v", name, err)
		}
		if strings.Contains(err.Error(), anthropic) || strings.Contains(err.Error(), aws) {
			t.Fatalf("%s: the error quotes a credential: %v", name, err)
		}
		if e.text(t) != text {
			t.Fatalf("%s: a refused edit changed the file: %q", name, e.text(t))
		}
	}
	// Both kept, in order, with text between and around them.
	if err := e.edit(t, old, "a := \""+holder+"\" // 1\nb := \""+awsHolder+"\" // 2", false); err != nil {
		t.Fatal(err)
	}
	if got, want := e.text(t), "a := \""+anthropic+"\" // 1\nb := \""+aws+"\" // 2\n"; got != want {
		t.Fatalf("file: %q", got)
	}
}

// Every match is its own span: each keeps the credential it had.
func TestFsEditReplaceAllKeepsEachCredential(t *testing.T) {
	other := "sk-ant-" + "bbbbbbbb2222"
	e := newEditView(t, "x = \""+anthropic+"\"\ny = \""+other+"\"\n")
	err := e.edit(t, `= "`+holder+`"`, `= "`+holder+`" # k`, false)
	if err == nil || !strings.Contains(err.Error(), "occurs 2 times") {
		t.Fatalf("two matches without replace_all: %v", err)
	}
	if err := e.edit(t, `= "`+holder+`"`, `= "`+holder+`" # k`, true); err != nil {
		t.Fatal(err)
	}
	if got, want := e.text(t), "x = \""+anthropic+"\" # k\ny = \""+other+"\" # k\n"; got != want {
		t.Fatalf("file: %q", got)
	}
}

// A match that begins or ends inside a placeholder names a half of it, which
// stands for nothing.
func TestFsEditRefusesHalfAPlaceholder(t *testing.T) {
	text := "body := \"" + anthropic + "\"\n"
	e := newEditView(t, text)
	for _, old := range []string{`anthropic-key]"`, `body := "[REDACTED anth`} {
		err := e.edit(t, old, "x", false)
		if err == nil || !strings.Contains(err.Error(), "inside a "+holder+" placeholder") {
			t.Fatalf("%q: %v", old, err)
		}
	}
	if e.text(t) != text {
		t.Fatal("a refused edit changed the file")
	}
}

// A placeholder the file holds as text reads the same as one redaction
// wrote, so old that names it alone is ambiguous, and an edit of the line
// that holds only text takes new as text, as every edit did.
func TestFsEditTellsFileTextFromACredential(t *testing.T) {
	e := newEditView(t, "a := \""+holder+"\"\nb := \""+anthropic+"\"\n")
	if err := e.edit(t, `"`+holder+`"`, "1", false); err == nil || !strings.Contains(err.Error(), "occurs 2 times") {
		t.Fatalf("the two places the model reads as one: %v", err)
	}
	if err := e.edit(t, `a := "`+holder+`"`, `a := "`+holder+`" + "!"`, false); err != nil {
		t.Fatal(err)
	}
	if got, want := e.text(t), "a := \""+holder+"\" + \"!\"\nb := \""+anthropic+"\"\n"; got != want {
		t.Fatalf("file: %q", got)
	}
}

// A failed match is explained from what the model reads: the lines it quotes
// carry placeholders, never the credential. An old that spells the credential
// still matches the file's own text, as it always did.
func TestFsEditQuotesTheViewAndKeepsLiteralMatches(t *testing.T) {
	e := newEditView(t, "body := \""+anthropic+"\"\n")
	err := e.edit(t, `body  :=  "`+holder+`"`, "x", false)
	if err == nil || !strings.Contains(err.Error(), "ignoring whitespace it matches lines 1-1") || !strings.Contains(err.Error(), "body := \""+holder+"\"") {
		t.Fatalf("whitespace hint: %v", err)
	}
	if strings.Contains(err.Error(), anthropic) {
		t.Fatalf("the error quotes the credential: %v", err)
	}
	if err := e.edit(t, `body := "`+anthropic+`"`, `body := 1`, false); err != nil {
		t.Fatalf("a literal that equals the file's text: %v", err)
	}
	if e.text(t) != "body := 1\n" {
		t.Fatalf("file: %q", e.text(t))
	}
}

// Edits in a list each apply to the text the one before left, placeholders
// included, and a PEM block that spans lines is one placeholder.
func TestFsEditListAndMultiLineCredentials(t *testing.T) {
	pem := "-----BEGIN " + "PRIVATE KEY-----\nMIIBVQIBADANBgkqhkiG9w0B\n-----END " + "PRIVATE KEY-----"
	e := newEditView(t, "key = `"+pem+"`\nname = \""+anthropic+"\"\n")
	_, err := e.eval(t, `(fs-edit "fixture.go" "" "" #f (list `+
		`(list (cons "old" `+q("key = `[REDACTED pem]`")+`) (cons "new" `+q("key = `[REDACTED pem]` // private")+`))`+
		`(list (cons "old" `+q("// private\nname = \""+holder+"\"")+`) (cons "new" `+q("// private\nname = \""+holder+"\" // fixture")+`))))`)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := e.text(t), "key = `"+pem+"` // private\nname = \""+anthropic+"\" // fixture\n"; got != want {
		t.Fatalf("file:\n%s\nwant:\n%s", got, want)
	}
}
