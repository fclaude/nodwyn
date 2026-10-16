package prim

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fclaude/nodwyn/internal/extract"
	"github.com/fclaude/nodwyn/internal/guard"
	nlog "github.com/fclaude/nodwyn/internal/log"
	"github.com/fclaude/nodwyn/internal/scheme"
)

// fakePandoc writes a stand-in for pandoc into its own directory: it
// records each run's arguments (one per line, a run ending in "--") and
// input, answers a --to=json run with tree.json from that directory and
// any other with FAKE-DOCUMENT. script replaces its body.
type fakePandoc struct {
	dir, path string
}

func newFakePandoc(t *testing.T, script string) *fakePandoc {
	t.Helper()
	dir := t.TempDir()
	if script == "" {
		script = `for a in "$@"; do printf '%s\n' "$a" >> 'DIR/argv'; done
echo -- >> 'DIR/argv'
case "$*" in
*--to=json*) cat > 'DIR/input1'; cat 'DIR/tree.json' ;;
*) cat > 'DIR/input2'; printf 'FAKE-DOCUMENT' ;;
esac`
	}
	path := filepath.Join(dir, "pandoc")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+strings.ReplaceAll(script, "DIR", dir)+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return &fakePandoc{dir: dir, path: path}
}

// runs returns the argument lists of the runs so far.
func (f *fakePandoc) runs() [][]string {
	raw, _ := os.ReadFile(filepath.Join(f.dir, "argv"))
	var out [][]string
	var cur []string
	for _, line := range strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n") {
		switch line {
		case "":
		case "--":
			out = append(out, cur)
			cur = nil
		default:
			cur = append(cur, line)
		}
	}
	return out
}

func (f *fakePandoc) read(name string) string {
	raw, _ := os.ReadFile(filepath.Join(f.dir, name))
	return string(raw)
}

// exportHarness is newHarness with doc-export's pandoc and the version
// store set, and optionally mounts.
func exportHarness(t *testing.T, pandoc string, mounts ...guard.Mount) *harness {
	t.Helper()
	root := t.TempDir()
	root, _ = filepath.EvalSymlinks(root)
	h := &harness{root: root, logBuf: &bytes.Buffer{}, answer: true}
	h.in = scheme.New()
	o := DefaultOptions(root)
	o.Pandoc = pandoc
	o.Mounts = mounts
	logger := recorder{nlog.New(h.logBuf, "test")}
	o.Versions = &Versions{Dir: VersionsDir(root), Skip: InGitWorkTree(root), Record: func(v Version) {
		logger.Record(nlog.KindFileVersion, nlog.FileVersion{Path: v.Path, Hash: v.Hash, Bytes: v.Bytes})
	}}
	if err := Register(h.in, o); err != nil {
		t.Fatal(err)
	}
	confirm := guard.ConfirmFunc(func(_ context.Context, p guard.Prompt) (bool, error) {
		h.prompts = append(h.prompts, p)
		return h.answer, nil
	})
	h.in.SetGate(guard.New(confirm, logger, Checkers(o, nil, nil)...))
	return h
}

// imageTree is pandoc's JSON for a paragraph of images with these sources.
func imageTree(sources ...string) string {
	var inlines []string
	for i, src := range sources {
		inlines = append(inlines, fmt.Sprintf(`{"t":"Image","c":[["",[],[]],[{"t":"Str","c":"figure%d"}],[%q,""]]}`, i, src))
	}
	return `{"pandoc-api-version":[1,23,1,1],"meta":{},"blocks":[{"t":"Para","c":[` + strings.Join(inlines, `,{"t":"Space"},`) + `]},{"t":"Table","c":[0.25]}]}`
}

// doc-export runs pandoc twice with fixed arguments, the first sandboxed
// and reading the source on stdin; Go keeps only the images that are files
// in the repository, writes the target through the guard (a confirm with a
// preview), and keeps the target's previous version.
func TestDocExportRunsPandocWithFixedArguments(t *testing.T) {
	pandoc := newFakePandoc(t, "")
	h := exportHarness(t, pandoc.path)
	os.WriteFile(filepath.Join(h.root, "notes.md"), []byte("# Notes\n\n![a figure](fig.png)\n"), 0o644)
	os.WriteFile(filepath.Join(h.root, "fig.png"), []byte("\x89PNG"), 0o644)
	os.MkdirAll(filepath.Join(h.root, "keys"), 0o755)
	os.WriteFile(filepath.Join(h.root, "keys", "id_rsa.png"), []byte("key"), 0o644)
	// A link named like an image is the file it points at.
	os.WriteFile(filepath.Join(h.root, ".env"), []byte("TOKEN=link-canary\n"), 0o600)
	if err := os.Symlink(".env", filepath.Join(h.root, "pic.png")); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(filepath.Dir(h.root), "outside.png")
	os.WriteFile(outside, []byte("\x89PNG"), 0o644)
	t.Cleanup(func() { os.Remove(outside) })
	os.WriteFile(filepath.Join(pandoc.dir, "tree.json"), []byte(imageTree("fig.png", "https://example.com/x.png", "../"+filepath.Base(outside), "keys/id_rsa.png", "pic.png", "data:image/png;base64,AAAA", "notes.md", "missing.png")), 0o644)
	os.WriteFile(filepath.Join(h.root, "notes.docx"), []byte("old document"), 0o644)

	v := h.mustEval(t, `(doc-export "notes.md" "notes.docx")`)
	got := v.S
	if !strings.HasPrefix(got, "wrote notes.docx (Word document, 13 B) from notes.md") || !strings.Contains(got, "6 image(s) left out") {
		t.Fatalf("result: %q", got)
	}
	runs := pandoc.runs()
	want := [][]string{{"--sandbox", "--from=markdown", "--to=json"}, {"--from=json", "--to=docx", "--standalone", "--output=-"}}
	if fmt.Sprint(runs) != fmt.Sprint(want) {
		t.Fatalf("pandoc runs:\n got %q\nwant %q", runs, want)
	}
	if pandoc.read("input1") != "# Notes\n\n![a figure](fig.png)\n" {
		t.Fatalf("first run read %q", pandoc.read("input1"))
	}
	var tree struct {
		Blocks []struct {
			T string `json:"t"`
			C json.RawMessage
		} `json:"blocks"`
	}
	if err := json.Unmarshal([]byte(pandoc.read("input2")), &tree); err != nil {
		t.Fatal(err)
	}
	var para []map[string]any
	json.Unmarshal(tree.Blocks[0].C, &para)
	var kinds []string
	for _, in := range para {
		if in["t"] == "Space" {
			continue
		}
		kind := in["t"].(string)
		if kind == "Image" {
			kind += ":" + in["c"].([]any)[2].([]any)[0].(string)
		}
		kinds = append(kinds, kind)
	}
	wantKinds := []string{"Image:" + filepath.Join(h.root, "fig.png"), "Span", "Span", "Span", "Span", "Image:data:image/png;base64,AAAA", "Span", "Span"}
	if fmt.Sprint(kinds) != fmt.Sprint(wantKinds) {
		t.Fatalf("vetted images:\n got %v\nwant %v", kinds, wantKinds)
	}
	if string(tree.Blocks[1].C) != "[0.25]" {
		t.Fatalf("numbers changed in the tree: %s", tree.Blocks[1].C)
	}
	for _, why := range []string{"https://example.com/x.png (a link", "(outside the repository)", "id_rsa.png (a credential file)", "pic.png (a credential file)", "notes.md (not an image)", "missing.png (no such file)"} {
		if !strings.Contains(got, why) {
			t.Fatalf("result %q does not say %q", got, why)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(h.root, "notes.docx")); string(b) != "FAKE-DOCUMENT" {
		t.Fatalf("target: %q", b)
	}
	if len(h.prompts) != 1 {
		t.Fatalf("prompts: %+v", h.prompts)
	}
	p := h.prompts[0]
	if p.Path != "notes.docx" || !strings.Contains(strings.Join(p.Reasons, "; "), "changes notes.docx") ||
		!strings.Contains(p.Detail, "notes.md (Markdown, 30 B) → notes.docx (Word document), converted by pandoc") || !strings.Contains(p.Detail, "replaces notes.docx (12 B)") {
		t.Fatalf("prompt: %+v", p)
	}
	versions, _ := ListVersions(VersionsDir(h.root), filepath.Join(h.root, "notes.docx"))
	if len(versions) != 1 {
		t.Fatalf("versions: %+v", versions)
	}
	if old, _ := ReadVersion(VersionsDir(h.root), versions[0].Hash); string(old) != "old document" {
		t.Fatalf("kept %q", old)
	}
}

// An HTML page gets the target's name as its title when the document has
// none, instead of pandoc's "-", and its images as the document wrote
// them, since an HTML writer reads none; LaTeX is read as LaTeX.
func TestDocExportHTMLTitleAndLaTeX(t *testing.T) {
	pandoc := newFakePandoc(t, "")
	h := exportHarness(t, pandoc.path)
	os.WriteFile(filepath.Join(h.root, "paper.tex"), []byte(`\section{One}`), 0o644)
	os.WriteFile(filepath.Join(pandoc.dir, "tree.json"), []byte(imageTree("fig.png", "https://example.com/x.png")), 0o644)
	got := h.mustEval(t, `(doc-export "paper.tex" "site/paper.html")`)
	runs := pandoc.runs()
	if len(runs) != 2 || runs[0][1] != "--from=latex" || runs[1][1] != "--to=html5" {
		t.Fatalf("runs: %q", runs)
	}
	tree := pandoc.read("input2")
	// A page refers to its images: a file in the folder stays as it is, and
	// a link elsewhere, which the page would load when opened, is left out.
	if !strings.Contains(tree, `"pagetitle":{"c":"paper","t":"MetaString"}`) || !strings.Contains(tree, `["fig.png",""]`) || strings.Contains(tree, "https://example.com/x.png") {
		t.Fatalf("tree: %s", tree)
	}
	if !strings.Contains(got.S, "left out") || !strings.Contains(got.S, "https://example.com/x.png") {
		t.Fatalf("result: %q", got.S)
	}
	if b, _ := os.ReadFile(filepath.Join(h.root, "site", "paper.html")); string(b) != "FAKE-DOCUMENT" {
		t.Fatalf("target: %q", b)
	}
}

// The guard judges the source as a read and the target as a write: both
// pinned, a read-only mount may supply the source but not take the target,
// a protected target always asks a person, and a declined confirm writes
// nothing and runs nothing.
func TestDocExportIsGuarded(t *testing.T) {
	pandoc := newFakePandoc(t, "")
	ref, _ := filepath.EvalSymlinks(t.TempDir())
	h := exportHarness(t, pandoc.path, guard.Mount{Path: ref})
	os.WriteFile(filepath.Join(pandoc.dir, "tree.json"), []byte(`{"pandoc-api-version":[1,23,1,1],"meta":{},"blocks":[]}`), 0o644)
	os.WriteFile(filepath.Join(h.root, "notes.md"), []byte("# Notes\n"), 0o644)
	os.WriteFile(filepath.Join(ref, "brief.md"), []byte("# Brief\n"), 0o644)
	outside := filepath.Join(filepath.Dir(h.root), "outside-"+filepath.Base(h.root)+".md")
	os.WriteFile(outside, []byte("# Outside\n"), 0o644)
	t.Cleanup(func() { os.Remove(outside) })

	for _, c := range []struct{ src, want string }{
		{`(doc-export "notes.md" "../out.docx")`, "outside the repository"},
		{`(doc-export "` + outside + `" "out.docx")`, "outside the repository"},
		{`(doc-export "notes.md" "` + filepath.Join(ref, "out.docx") + `")`, "read-only"},
		{`(doc-export ".env.md" "out.docx")`, "credential-file denylist"},
	} {
		_, err := h.eval(t, c.src)
		denied(t, err, c.want)
	}
	if len(h.prompts) != 0 || len(pandoc.runs()) != 0 {
		t.Fatalf("a denied export asked or ran: %+v %q", h.prompts, pandoc.runs())
	}

	h.answer = false
	if _, err := h.eval(t, `(doc-export "notes.md" "out.docx")`); err == nil {
		t.Fatal("a declined export ran")
	}
	if _, err := os.Stat(filepath.Join(h.root, "out.docx")); err == nil || len(pandoc.runs()) != 0 {
		t.Fatal("a declined export wrote or ran")
	}

	h.answer = true
	h.prompts = nil
	h.mustEval(t, `(doc-export "`+filepath.Join(ref, "brief.md")+`" "brief.docx")`)
	if b, _ := os.ReadFile(filepath.Join(h.root, "brief.docx")); string(b) != "FAKE-DOCUMENT" || pandoc.read("input1") != "# Brief\n" {
		t.Fatalf("export from a read-only mount: %q", b)
	}
	h.mustEval(t, `(doc-export "notes.md" ".nodwyn/notes.docx")`)
	if p := h.prompts[len(h.prompts)-1]; !p.Security {
		t.Fatalf("a protected target was not a security confirm: %+v", p)
	}
}

// Without pandoc, the error names what to install, and the confirm says
// the export will fail; a pandoc too old for --sandbox, a pandoc that
// fails, and the formats doc-export does not write are errors that say so.
func TestDocExportErrors(t *testing.T) {
	h := exportHarness(t, "nodwyn-test-no-such-pandoc")
	os.WriteFile(filepath.Join(h.root, "notes.md"), []byte("# Notes\n"), 0o644)
	_, err := h.eval(t, `(doc-export "notes.md" "notes.docx")`)
	if err == nil || !strings.Contains(err.Error(), "pandoc is not installed") || !strings.Contains(err.Error(), "dnf install pandoc") {
		t.Fatalf("missing pandoc: %v", err)
	}
	if p := h.prompts[0]; !strings.Contains(p.Detail, "pandoc is not installed: the export will fail") {
		t.Fatalf("preview: %q", p.Detail)
	}
	if _, err := os.Stat(filepath.Join(h.root, "notes.docx")); err == nil {
		t.Fatal("wrote a target without pandoc")
	}

	old := newFakePandoc(t, `echo 'Unknown option --sandbox.' >&2; exit 6`)
	h = exportHarness(t, old.path)
	os.WriteFile(filepath.Join(h.root, "notes.md"), []byte("# Notes\n"), 0o644)
	if _, err := h.eval(t, `(doc-export "notes.md" "notes.docx")`); err == nil || !strings.Contains(err.Error(), "pandoc 2.15") {
		t.Fatalf("old pandoc: %v", err)
	}
	broken := newFakePandoc(t, `cat > /dev/null; echo 'Error at "source" (line 3): unexpected end' >&2; exit 64`)
	h = exportHarness(t, broken.path)
	os.WriteFile(filepath.Join(h.root, "notes.tex"), []byte(`\begin{x}`), 0o644)
	if _, err := h.eval(t, `(doc-export "notes.tex" "notes.odt")`); err == nil || !strings.Contains(err.Error(), "pandoc failed (exit 64)") || !strings.Contains(err.Error(), "unexpected end") {
		t.Fatalf("failing pandoc: %v", err)
	}
	for src, want := range map[string]string{
		`(doc-export "notes.tex" "notes.pdf")`:  "latexmk",
		`(doc-export "notes.txt" "notes.docx")`: "converts Markdown (.md) or LaTeX (.tex)",
		`(doc-export "notes.tex" "notes.rtf")`:  "writes .docx, .odt or .html",
	} {
		if _, err := h.eval(t, src); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: %v", src, err)
		}
	}
}

// With the real pandoc, when it is installed: Markdown becomes a .docx
// whose text is the document's, and a LaTeX \input of another file reads
// nothing, since the source is read in pandoc's sandbox.
func TestDocExportWithRealPandoc(t *testing.T) {
	if _, err := exec.LookPath("pandoc"); err != nil {
		t.Skip("pandoc is not installed")
	}
	h := exportHarness(t, "")
	os.WriteFile(filepath.Join(h.root, "notes.md"), []byte("# Weekly notes\n\nThe *build* is green.\n"), 0o644)
	h.mustEval(t, `(doc-export "notes.md" "notes.docx")`)
	res, err := extract.File(context.Background(), filepath.Join(h.root, "notes.docx"), "notes.docx")
	if err != nil || !strings.Contains(res.Text, "Weekly notes") || !strings.Contains(res.Text, "The build is green.") {
		t.Fatalf("docx: %+v %v", res, err)
	}
	h.mustEval(t, `(doc-export "notes.md" "notes.html")`)
	if page, _ := os.ReadFile(filepath.Join(h.root, "notes.html")); !bytes.Contains(page, []byte("<title>")) || !bytes.Contains(page, []byte("Weekly notes")) {
		t.Fatalf("html: %s", page)
	}
	secret := filepath.Join(t.TempDir(), "included.tex")
	os.WriteFile(secret, []byte("INCLUDED-TEXT\n"), 0o644)
	os.WriteFile(filepath.Join(h.root, "paper.tex"), []byte("\\begin{document}\nBefore \\input{"+strings.TrimSuffix(secret, ".tex")+"}\n\\end{document}\n"), 0o644)
	got := h.mustEval(t, `(doc-export "paper.tex" "paper.html")`)
	if page, _ := os.ReadFile(filepath.Join(h.root, "paper.html")); !bytes.Contains(page, []byte("Before")) || bytes.Contains(page, []byte("INCLUDED-TEXT")) {
		t.Fatalf("the sandbox let an \\input through: %s", page)
	}
	if !strings.Contains(got.S, "Could not load include file") {
		t.Fatalf("pandoc's warning is not in the result: %q", got.S)
	}
}

// Raw content in the source (an OpenXML field, an HTML frame) reaches the
// writer as literal code, never as markup it would interpret.
func TestDocExportKeepsRawContentAsText(t *testing.T) {
	pandoc := newFakePandoc(t, "")
	h := exportHarness(t, pandoc.path)
	os.WriteFile(filepath.Join(h.root, "notes.md"), []byte("hi"), 0o644)
	raw := `{"pandoc-api-version":[1,23,1,1],"meta":{},"blocks":[` +
		`{"t":"RawBlock","c":["openxml","<w:fldSimple w:instr=\"INCLUDEPICTURE http://evil.example/x\"/>"]},` +
		`{"t":"Para","c":[{"t":"RawInline","c":["html","<iframe src=\"http://evil.example/\"></iframe>"]}]}]}`
	os.WriteFile(filepath.Join(pandoc.dir, "tree.json"), []byte(raw), 0o644)
	for _, target := range []string{"out/notes.docx", "out/notes.html"} {
		got := h.mustEval(t, `(doc-export "notes.md" "`+target+`")`)
		tree := pandoc.read("input2")
		if strings.Contains(tree, "RawBlock") || strings.Contains(tree, "RawInline") || !strings.Contains(tree, `"t":"CodeBlock"`) || !strings.Contains(tree, `"t":"Code"`) {
			t.Fatalf("%s: raw content reached the writer: %s", target, tree)
		}
		if !strings.Contains(got.S, "raw content written into the source (openxml, html) is shown as text") {
			t.Fatalf("%s: result %q", target, got.S)
		}
	}
}
