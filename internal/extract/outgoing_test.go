package extract

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// AllText reads every text part of a package, the markup and the joined
// runs, passes over images and fonts, and refuses what it cannot check.
func TestAllTextReadsEveryPart(t *testing.T) {
	doc := `<w:document><w:body><w:p><w:r><w:t>token sk-ant-</w:t></w:r><w:r><w:t>api03-Abc</w:t></w:r></w:p><w:p><w:r><w:t>next</w:t></w:r></w:p></w:body></w:document>`
	content, _ := os.ReadFile(zipFile(t, "a.docx", map[string]string{
		"word/document.xml":            doc,
		"word/header1.xml":             `<w:hdr><w:p><w:r><w:t>in the header</w:t></w:r></w:p></w:hdr>`,
		"word/_rels/document.xml.rels": `<Relationships><Relationship Target="https://example.com/?k=v"/></Relationships>`,
		"docProps/core.xml":            `<cp:coreProperties><dc:creator>someone</dc:creator></cp:coreProperties>`,
		"word/media/image1.png":        "\x89PNG pixels-not-read",
		"word/fonts/font1.odttf":       "glyphs-not-read",
	}))
	text, err := AllText(content, "a.docx")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"token sk-ant-api03-Abc\n", "in the header", "https://example.com/?k=v", "someone", "<w:t>next</w:t>"} {
		if !strings.Contains(text, want) {
			t.Fatalf("%q is not in:\n%s", want, text)
		}
	}
	if strings.Contains(text, "api03-Abcnext") || strings.Contains(text, "not-read") {
		t.Fatalf("paragraphs joined, or binary parts read:\n%s", text)
	}

	embedded, _ := os.ReadFile(zipFile(t, "b.docx", map[string]string{"word/document.xml": doc, "word/embeddings/oleObject1.bin": "anything"}))
	if _, err := AllText(embedded, "b.docx"); err == nil || !strings.Contains(err.Error(), "word/embeddings/oleObject1.bin") {
		t.Fatalf("an embedded file: %v", err)
	}
	odt, _ := os.ReadFile(zipFile(t, "c.odt", map[string]string{"mimetype": "application/vnd.oasis.opendocument.text", "content.xml": `<text:p>hello</text:p>`}))
	if text, err := AllText(odt, "c.odt"); err != nil || !strings.Contains(text, "hello") || !strings.Contains(text, "opendocument") {
		t.Fatalf("odt: %q %v", text, err)
	}
	if text, err := AllText([]byte(`<p title="x">page</p>`), "page.html"); err != nil || text != `<p title="x">page</p>` {
		t.Fatalf("html: %q %v", text, err)
	}
	if _, err := AllText([]byte("x"), "a.pdf"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("pdf: %v", err)
	}
	if _, err := AllText([]byte("not a zip"), "d.docx"); err == nil {
		t.Fatal("a broken package was read")
	}
}
