// Package extract turns an attached file into text, deterministically and by
// type (ADR 22 §3): text formats as they are, Office documents from their
// XML, PDF through pdftotext when it is installed. It decodes; it never
// interprets. What it returns is data for the model, never instructions.
// An image is not turned into text: ReadImage checks it (image.go) so it
// can be sent as it is.
package extract

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	zpath "path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// MaxInput is the largest file extract reads.
const MaxInput = 20 << 20

// maxUnpacked bounds what an Office file may inflate to.
const maxUnpacked = 64 << 20

// Result is a file's text and what kind of file it was.
type Result struct {
	Type  string // "text", "pdf", "docx", "xlsx", "pptx"
	Text  string
	Pages int // pages, slides or sheets; 0 when the notion does not apply
}

// ErrUnsupported is returned for a type extract cannot read.
var ErrUnsupported = errors.New("extract: unsupported file type")

var textTypes = map[string]bool{
	".txt": true, ".md": true, ".markdown": true, ".csv": true, ".tsv": true, ".json": true, ".jsonl": true,
	".xml": true, ".yaml": true, ".yml": true, ".toml": true, ".ini": true, ".cfg": true, ".conf": true, ".log": true,
	".html": true, ".htm": true, ".css": true, ".svg": true, ".tex": true, ".bib": true, ".rst": true, ".org": true,
	".go": true, ".py": true, ".js": true, ".mjs": true, ".ts": true, ".tsx": true, ".jsx": true, ".rs": true, ".java": true,
	".kt": true, ".swift": true, ".c": true, ".h": true, ".cc": true, ".cpp": true, ".hpp": true, ".rb": true, ".php": true,
	".sh": true, ".zsh": true, ".bash": true, ".sql": true, ".scm": true, ".lisp": true, ".el": true, ".lua": true, ".r": true,
	".ics": true, ".vcf": true, ".eml": true, ".srt": true, ".vtt": true,
}

var imageTypes = map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true, ".heic": true, ".bmp": true, ".tiff": true}

// File extracts the text of the file at path; name (the file's original
// name) decides its type.
func File(ctx context.Context, path, name string) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return Result{}, err
	}
	if !info.Mode().IsRegular() {
		return Result{}, fmt.Errorf("extract: %s is not a regular file", name)
	}
	if info.Size() > MaxInput {
		return Result{}, fmt.Errorf("extract: %s is %d MB; attachments are at most %d MB", name, info.Size()>>20, MaxInput>>20)
	}
	ext := strings.ToLower(filepath.Ext(name))
	switch {
	case ext == ".pdf":
		return pdf(ctx, path)
	case ext == ".docx":
		return docx(ctx, path)
	case ext == ".xlsx":
		return xlsx(ctx, path)
	case ext == ".pptx":
		return pptx(ctx, path)
	case imageTypes[ext]:
		return Result{}, fmt.Errorf("%w: %s is an image, which is sent as an image (ReadImage), never as text; nothing is OCRed", ErrUnsupported, name)
	case textTypes[ext] || ext == "":
		raw, err := os.ReadFile(path)
		if err != nil {
			return Result{}, err
		}
		raw = bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf"))
		if !utf8.Valid(raw) || bytes.IndexByte(raw, 0) >= 0 {
			return Result{}, fmt.Errorf("%w: %s is not UTF-8 text", ErrUnsupported, name)
		}
		return Result{Type: "text", Text: string(raw)}, nil
	}
	return Result{}, fmt.Errorf("%w: %s (%s)", ErrUnsupported, name, ext)
}

// pdf runs pdftotext (poppler), which nodwyn does not bundle.
func pdf(ctx context.Context, path string) (Result, error) {
	tool, err := exec.LookPath("pdftotext")
	if err != nil {
		return Result{}, fmt.Errorf("%w: reading PDF needs pdftotext (brew install poppler, or apt install poppler-utils)", ErrUnsupported)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, tool, "-layout", "-enc", "UTF-8", "-q", path, "-")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")}
	var out bytes.Buffer
	cmd.Stdout = &limited{&out, maxUnpacked}
	if err := cmd.Run(); err != nil {
		return Result{}, fmt.Errorf("extract: pdftotext: %w", err)
	}
	text := strings.ToValidUTF8(out.String(), "�")
	pages := strings.Count(text, "\f")
	if pages == 0 && strings.TrimSpace(text) != "" {
		pages = 1
	}
	text = strings.ReplaceAll(text, "\f", "\n\n")
	if strings.TrimSpace(text) == "" {
		return Result{}, fmt.Errorf("%w: this PDF has no text layer (a scan?)", ErrUnsupported)
	}
	return Result{Type: "pdf", Text: text, Pages: pages}, nil
}

type limited struct {
	w *bytes.Buffer
	n int
}

func (l *limited) Write(p []byte) (int, error) {
	if l.w.Len()+len(p) > l.n {
		return 0, errors.New("extract: output too large")
	}
	return l.w.Write(p)
}

// openZip opens an Office file and checks it cannot inflate past the bound.
func openZip(path string) (*zip.ReadCloser, error) {
	r, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("extract: not a valid Office file: %w", err)
	}
	if err := unpackBound(&r.Reader); err != nil {
		r.Close()
		return nil, err
	}
	return r, nil
}

// unpackBound refuses a package whose parts inflate past maxUnpacked.
func unpackBound(r *zip.Reader) error {
	var total uint64
	for _, f := range r.File {
		total += f.UncompressedSize64
	}
	if total > maxUnpacked {
		return fmt.Errorf("extract: the file unpacks to more than %d MB", maxUnpacked>>20)
	}
	return nil
}

func readZip(r *zip.ReadCloser, name string) ([]byte, error) {
	for _, f := range r.File {
		if f.Name == name {
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			defer rc.Close()
			return io.ReadAll(io.LimitReader(rc, maxUnpacked))
		}
	}
	return nil, os.ErrNotExist
}

// walkXML calls on for each start element, end element and character data.
func walkXML(raw []byte, on func(tok xml.Token)) error {
	return walkXMLContext(context.Background(), raw, on)
}

// walkXMLContext checks cancellation between tokens so large Office parts do
// not keep parsing after their attachment request has ended.
func walkXMLContext(ctx context.Context, raw []byte, on func(tok xml.Token)) error {
	d := xml.NewDecoder(bytes.NewReader(raw))
	d.Strict = false
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		tok, err := d.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		on(tok)
	}
}

// docx reads word/document.xml: paragraphs, tabs, breaks and text runs.
func docx(ctx context.Context, path string) (Result, error) {
	r, err := openZip(path)
	if err != nil {
		return Result{}, err
	}
	defer r.Close()
	raw, err := readZip(r, "word/document.xml")
	if err != nil {
		return Result{}, fmt.Errorf("extract: no document in this .docx")
	}
	var b strings.Builder
	inText := false
	err = walkXMLContext(ctx, raw, func(tok xml.Token) {
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "t":
				inText = true
			case "tab":
				b.WriteByte('\t')
			case "br", "cr":
				b.WriteByte('\n')
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inText = false
			case "p":
				b.WriteByte('\n')
			}
		case xml.CharData:
			if inText {
				b.Write(t)
			}
		}
	})
	if err != nil {
		return Result{}, fmt.Errorf("extract: .docx: %w", err)
	}
	return Result{Type: "docx", Text: b.String()}, nil
}

// pptx reads each slide's text in the order of the deck, which
// presentation.xml gives and the numbers in the slides' file names do not
// once slides are moved, added or deleted.
func pptx(ctx context.Context, path string) (Result, error) {
	r, err := openZip(path)
	if err != nil {
		return Result{}, err
	}
	defer r.Close()
	var slides []string
	parts, err := orderedParts(ctx, r, "ppt/presentation.xml", "ppt/_rels/presentation.xml.rels", "sldId")
	if err != nil {
		return Result{}, err
	}
	for _, p := range parts {
		slides = append(slides, p.path)
	}
	if len(slides) == 0 {
		slides = numbered(r, "ppt/slides/slide", ".xml")
	}
	var b strings.Builder
	for i, name := range slides {
		raw, err := readZip(r, name)
		if err != nil {
			continue
		}
		fmt.Fprintf(&b, "## Slide %d\n", i+1)
		inText := false
		err = walkXMLContext(ctx, raw, func(tok xml.Token) {
			switch t := tok.(type) {
			case xml.StartElement:
				if t.Name.Local == "t" {
					inText = true
				}
			case xml.EndElement:
				switch t.Name.Local {
				case "t":
					inText = false
				case "p":
					b.WriteByte('\n')
				}
			case xml.CharData:
				if inText {
					b.Write(t)
				}
			}
		})
		if err != nil {
			return Result{}, err
		}
		b.WriteByte('\n')
	}
	return Result{Type: "pptx", Text: b.String(), Pages: len(slides)}, nil
}

// numbered lists zip entries prefix<N>suffix in numeric order.
func numbered(r *zip.ReadCloser, prefix, suffix string) []string {
	type item struct {
		n    int
		name string
	}
	var items []item
	for _, f := range r.File {
		if strings.HasPrefix(f.Name, prefix) && strings.HasSuffix(f.Name, suffix) {
			if n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(f.Name, prefix), suffix)); err == nil {
				items = append(items, item{n, f.Name})
			}
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].n < items[j].n })
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.name
	}
	return out
}

// part is a document part named by a package's main part: the label it gives
// (a sheet's name) and where it is in the zip.
type part struct{ name, path string }

// orderedParts lists, in the order main gives them, the parts its elem
// elements (sheet, sldId) refer to by relationship id, resolved through the
// relationships in rels. A package that does not say (no such parts) gives
// none, and the caller goes by the file names.
func orderedParts(ctx context.Context, r *zip.ReadCloser, main, rels, elem string) ([]part, error) {
	targets := map[string]string{}
	if raw, err := readZip(r, rels); err == nil {
		base := zpath.Dir(zpath.Dir(rels)) // the folder the relationships are relative to
		err = walkXMLContext(ctx, raw, func(tok xml.Token) {
			t, ok := tok.(xml.StartElement)
			if !ok || t.Name.Local != "Relationship" {
				return
			}
			var id, target string
			external := false
			for _, a := range t.Attr {
				switch a.Name.Local {
				case "Id":
					id = a.Value
				case "Target":
					target = a.Value
				case "TargetMode":
					external = a.Value == "External"
				}
			}
			if id == "" || target == "" || external {
				return
			}
			if rest, ok := strings.CutPrefix(target, "/"); ok {
				targets[id] = rest
			} else {
				targets[id] = zpath.Join(base, target)
			}
		})
		if err != nil {
			return nil, err
		}
	}
	raw, err := readZip(r, main)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []part
	err = walkXMLContext(ctx, raw, func(tok xml.Token) {
		t, ok := tok.(xml.StartElement)
		if !ok || t.Name.Local != elem {
			return
		}
		var p part
		for _, a := range t.Attr {
			switch {
			case a.Name.Local == "name":
				p.name = a.Value
			case a.Name.Local == "id" && a.Name.Space != "": // r:id, not the slide's own id
				p.path = targets[a.Value]
			}
		}
		if p.path != "" {
			out = append(out, p)
		}
	})
	return out, err
}

// cellColumn is the zero-based column of a cell reference such as "C12",
// and false when it has none.
func cellColumn(ref string) (int, bool) {
	col, n := 0, 0
	for _, c := range ref {
		if c < 'A' || c > 'Z' {
			break
		}
		col, n = col*26+int(c-'A')+1, n+1
	}
	return col - 1, n > 0
}

// xlsx reads each sheet as tab-separated rows, with shared strings resolved.
// A row is given with its number and its cells in their columns, empty ones
// as empty fields, since a spreadsheet does not store them; a row with no
// cell holding anything is left out, its number a gap.
func xlsx(ctx context.Context, path string) (Result, error) {
	r, err := openZip(path)
	if err != nil {
		return Result{}, err
	}
	defer r.Close()
	var shared []string
	if raw, err := readZip(r, "xl/sharedStrings.xml"); err == nil {
		var cur strings.Builder
		inSI, inT := false, false
		err = walkXMLContext(ctx, raw, func(tok xml.Token) {
			switch t := tok.(type) {
			case xml.StartElement:
				switch t.Name.Local {
				case "si":
					inSI = true
					cur.Reset()
				case "t":
					inT = true
				}
			case xml.EndElement:
				switch t.Name.Local {
				case "si":
					inSI = false
					shared = append(shared, cur.String())
				case "t":
					inT = false
				}
			case xml.CharData:
				if inSI && inT {
					cur.Write(t)
				}
			}
		})
		if err != nil {
			return Result{}, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return Result{}, err
	}
	// The sheets in the workbook's order and under the names it gives them,
	// not in the order of the files' numbers, which are the order they were
	// made in.
	sheets, err := orderedParts(ctx, r, "xl/workbook.xml", "xl/_rels/workbook.xml.rels", "sheet")
	if err != nil {
		return Result{}, err
	}
	if len(sheets) == 0 {
		for i, name := range numbered(r, "xl/worksheets/sheet", ".xml") {
			sheets = append(sheets, part{name: fmt.Sprintf("Sheet %d", i+1), path: name})
		}
	}
	var b strings.Builder
	for _, sheet := range sheets {
		raw, err := readZip(r, sheet.path)
		if err != nil {
			continue
		}
		fmt.Fprintf(&b, "## %s\n", sheet.name)
		var row []string
		var cellType, value string
		var rowNumber, column int
		inValue := false
		var inline strings.Builder
		err = walkXMLContext(ctx, raw, func(tok xml.Token) {
			switch t := tok.(type) {
			case xml.StartElement:
				switch t.Name.Local {
				case "row":
					row = row[:0]
					rowNumber++
					for _, a := range t.Attr {
						if n, err := strconv.Atoi(a.Value); a.Name.Local == "r" && err == nil {
							rowNumber = n
						}
					}
					column = 0
				case "c":
					cellType, value = "", ""
					inline.Reset()
					for _, a := range t.Attr {
						switch a.Name.Local {
						case "t":
							cellType = a.Value
						case "r":
							if col, ok := cellColumn(a.Value); ok {
								column = col
							}
						}
					}
				case "v", "t":
					inValue = true
				}
			case xml.EndElement:
				switch t.Name.Local {
				case "v", "t":
					inValue = false
				case "c":
					cell := value
					switch cellType {
					case "s":
						if n, err := strconv.Atoi(strings.TrimSpace(value)); err == nil && n >= 0 && n < len(shared) {
							cell = shared[n]
						}
					case "inlineStr":
						cell = inline.String()
					}
					for len(row) <= column {
						row = append(row, "")
					}
					row[column] = strings.ReplaceAll(strings.ReplaceAll(cell, "\t", " "), "\n", " ")
					column++
				case "row":
					if line := strings.TrimRight(strings.Join(row, "\t"), "\t"); line != "" {
						fmt.Fprintf(&b, "%d: %s\n", rowNumber, line)
					}
				}
			case xml.CharData:
				if inValue {
					if cellType == "inlineStr" {
						inline.Write(t)
					} else {
						value += string(t)
					}
				}
			}
		})
		if err != nil {
			return Result{}, err
		}
		b.WriteByte('\n')
	}
	return Result{Type: "xlsx", Text: b.String(), Pages: len(sheets)}, nil
}
