package extract

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func zipFile(t *testing.T, name string, files map[string]string) string {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for n, body := range files {
		f, _ := w.Create(n)
		f.Write([]byte(body))
	}
	w.Close()
	path := filepath.Join(t.TempDir(), name)
	os.WriteFile(path, buf.Bytes(), 0o644)
	return path
}

func TestText(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notes.md")
	os.WriteFile(path, []byte("\xef\xbb\xbf# Notes\nhello\n"), 0o644)
	r, err := File(context.Background(), path, "notes.md")
	if err != nil || r.Type != "text" || r.Text != "# Notes\nhello\n" {
		t.Fatalf("%+v %v", r, err)
	}
	os.WriteFile(path, []byte{0xff, 0xfe, 0}, 0o644)
	if _, err := File(context.Background(), path, "notes.txt"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("binary as text: %v", err)
	}
	if _, err := File(context.Background(), path, "photo.jpg"); !errors.Is(err, ErrUnsupported) || !strings.Contains(err.Error(), "sent as an image") {
		t.Fatalf("image: %v", err)
	}
	if _, err := File(context.Background(), path, "tool.exe"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("exe: %v", err)
	}
}

func TestDocx(t *testing.T) {
	path := zipFile(t, "letter.docx", map[string]string{"word/document.xml": `<?xml version="1.0"?>
<w:document xmlns:w="w"><w:body><w:p><w:r><w:t>Dear </w:t></w:r><w:r><w:t xml:space="preserve">family,</w:t></w:r></w:p><w:p><w:r><w:t>Line</w:t><w:tab/><w:t>two</w:t></w:r></w:p></w:body></w:document>`})
	r, err := File(context.Background(), path, "letter.docx")
	if err != nil || r.Text != "Dear family,\nLine\ttwo\n" {
		t.Fatalf("%q %v", r.Text, err)
	}
}

func TestXlsx(t *testing.T) {
	path := zipFile(t, "receipts.xlsx", map[string]string{
		"xl/workbook.xml":            `<workbook xmlns:r="r"><sheets><sheet name="September" sheetId="1" r:id="rId1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="rId1" Target="worksheets/sheet1.xml"/></Relationships>`,
		"xl/sharedStrings.xml":       `<sst><si><t>Store</t></si><si><t>Amount</t></si><si><r><t>Corner </t></r><r><t>shop</t></r></si></sst>`,
		"xl/worksheets/sheet1.xml":   `<worksheet><sheetData><row><c t="s"><v>0</v></c><c t="s"><v>1</v></c></row><row><c t="s"><v>2</v></c><c><v>12.5</v></c><c t="inlineStr"><is><t>paid</t></is></c></row></sheetData></worksheet>`,
	})
	r, err := File(context.Background(), path, "receipts.xlsx")
	want := "## September\n1: Store\tAmount\n2: Corner shop\t12.5\tpaid\n\n"
	if err != nil || r.Text != want || r.Pages != 1 {
		t.Fatalf("%q %v", r.Text, err)
	}
}

// A spreadsheet does not store empty cells or rows, so a cell is placed by
// its reference: the blank column under Notes stays blank, and the row that
// was never stored is a gap in the numbers. Sheets come in the workbook's
// order under its names, whichever files hold them.
func TestXlsxPlacesCellsByReferenceAndSheetsByTheWorkbook(t *testing.T) {
	path := zipFile(t, "book.xlsx", map[string]string{
		"xl/workbook.xml": `<workbook xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets>` +
			`<sheet name="Summary" sheetId="2" r:id="rId2"/><sheet name="Data" sheetId="1" r:id="rId1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships>` +
			`<Relationship Id="rId1" Type="x/worksheet" Target="worksheets/sheet1.xml"/>` +
			`<Relationship Id="rId2" Type="x/worksheet" Target="/xl/worksheets/sheet2.xml"/></Relationships>`,
		"xl/worksheets/sheet1.xml": `<worksheet><sheetData>` +
			`<row r="1"><c r="A1" t="inlineStr"><is><t>name</t></is></c><c r="B1" t="inlineStr"><is><t>notes</t></is></c><c r="C1" t="inlineStr"><is><t>total</t></is></c></row>` +
			`<row r="3"><c r="A3" t="inlineStr"><is><t>x</t></is></c><c r="C3"><v>5</v></c></row>` +
			`<row r="4"><c r="A4"/></row></sheetData></worksheet>`,
		"xl/worksheets/sheet2.xml": `<worksheet><sheetData><row r="2"><c r="B2"><v>7</v></c></row></sheetData></worksheet>`,
	})
	r, err := File(context.Background(), path, "book.xlsx")
	want := "## Summary\n2: \t7\n\n## Data\n1: name\tnotes\ttotal\n3: x\t\t5\n\n"
	if err != nil || r.Text != want || r.Pages != 2 {
		t.Fatalf("got %q, want %q (%v)", r.Text, want, err)
	}
}

// Slides are in the deck's order, numbered by it, whatever their files are
// called.
func TestPptxFollowsThePresentationsOrder(t *testing.T) {
	path := zipFile(t, "deck.pptx", map[string]string{
		"ppt/presentation.xml": `<p:presentation xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><p:sldIdLst>` +
			`<p:sldId id="258" r:id="rId3"/><p:sldId id="256" r:id="rId2"/></p:sldIdLst></p:presentation>`,
		"ppt/_rels/presentation.xml.rels": `<Relationships><Relationship Id="rId2" Target="slides/slide1.xml"/><Relationship Id="rId3" Target="slides/slide3.xml"/></Relationships>`,
		"ppt/slides/slide1.xml":           `<p:sld><a:p><a:r><a:t>Was first</a:t></a:r></a:p></p:sld>`,
		"ppt/slides/slide3.xml":           `<p:sld><a:p><a:r><a:t>Was third</a:t></a:r></a:p></p:sld>`,
	})
	r, err := File(context.Background(), path, "deck.pptx")
	if err != nil || r.Pages != 2 || !strings.HasPrefix(r.Text, "## Slide 1\nWas third") || !strings.Contains(r.Text, "## Slide 2\nWas first") {
		t.Fatalf("%q %v", r.Text, err)
	}
}

func TestPptx(t *testing.T) {
	path := zipFile(t, "deck.pptx", map[string]string{
		"ppt/slides/slide10.xml": `<p:sld><a:p><a:r><a:t>Last</a:t></a:r></a:p></p:sld>`,
		"ppt/slides/slide2.xml":  `<p:sld><a:p><a:r><a:t>Second</a:t></a:r></a:p></p:sld>`,
	})
	r, err := File(context.Background(), path, "deck.pptx")
	if err != nil || r.Pages != 2 || !strings.Contains(r.Text, "## Slide 1\nSecond") || !strings.Contains(r.Text, "## Slide 2\nLast") {
		t.Fatalf("%q %v", r.Text, err)
	}
}

func TestZipBombRefused(t *testing.T) {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	f, _ := w.Create("word/document.xml")
	f.Write(bytes.Repeat([]byte("a"), maxUnpacked+1))
	w.Close()
	path := filepath.Join(t.TempDir(), "bomb.docx")
	os.WriteFile(path, buf.Bytes(), 0o644)
	if _, err := File(context.Background(), path, "bomb.docx"); err == nil {
		t.Fatal("inflated past the bound")
	}
}

// minimalPDF is a one-page PDF with a text layer.
func minimalPDF(text string) []byte {
	stream := fmt.Sprintf("BT /F1 24 Tf 72 720 Td (%s) Tj ET", text)
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objects))
	for i, o := range objects {
		offsets[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for _, off := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xref)
	return b.Bytes()
}

func TestPDF(t *testing.T) {
	if _, err := exec.LookPath("pdftotext"); err != nil {
		t.Skip("pdftotext not installed")
	}
	path := filepath.Join(t.TempDir(), "roadmap.pdf")
	os.WriteFile(path, minimalPDF("Roadmap Q4"), 0o644)
	r, err := File(context.Background(), path, "roadmap.pdf")
	if err != nil || r.Type != "pdf" || r.Pages != 1 || !strings.Contains(r.Text, "Roadmap Q4") {
		t.Fatalf("%+v %v", r, err)
	}
}
