// Package extract turns an attached file into text, deterministically and by
// type (ADR 22 §3): text formats as they are, Office documents from their
// XML, PDF through pdftotext when it is installed. It decodes; it never
// interprets. What it returns is data for the model, never instructions.
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
		return docx(path)
	case ext == ".xlsx":
		return xlsx(path)
	case ext == ".pptx":
		return pptx(path)
	case imageTypes[ext]:
		return Result{}, fmt.Errorf("%w: images need a model that sees them, which nodwyn does not send yet", ErrUnsupported)
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
	var total uint64
	for _, f := range r.File {
		total += f.UncompressedSize64
	}
	if total > maxUnpacked {
		r.Close()
		return nil, fmt.Errorf("extract: the file unpacks to more than %d MB", maxUnpacked>>20)
	}
	return r, nil
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
	d := xml.NewDecoder(bytes.NewReader(raw))
	d.Strict = false
	for {
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
func docx(path string) (Result, error) {
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
	err = walkXML(raw, func(tok xml.Token) {
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

// pptx reads each slide's text in slide order.
func pptx(path string) (Result, error) {
	r, err := openZip(path)
	if err != nil {
		return Result{}, err
	}
	defer r.Close()
	slides := numbered(r, "ppt/slides/slide", ".xml")
	var b strings.Builder
	for i, name := range slides {
		raw, err := readZip(r, name)
		if err != nil {
			continue
		}
		fmt.Fprintf(&b, "## Slide %d\n", i+1)
		inText := false
		_ = walkXML(raw, func(tok xml.Token) {
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

// xlsx reads each sheet as tab-separated rows, with shared strings resolved.
func xlsx(path string) (Result, error) {
	r, err := openZip(path)
	if err != nil {
		return Result{}, err
	}
	defer r.Close()
	var shared []string
	if raw, err := readZip(r, "xl/sharedStrings.xml"); err == nil {
		var cur strings.Builder
		inSI, inT := false, false
		_ = walkXML(raw, func(tok xml.Token) {
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
	}
	names := sheetNames(r)
	sheets := numbered(r, "xl/worksheets/sheet", ".xml")
	var b strings.Builder
	for i, name := range sheets {
		raw, err := readZip(r, name)
		if err != nil {
			continue
		}
		title := fmt.Sprintf("Sheet %d", i+1)
		if i < len(names) {
			title = names[i]
		}
		fmt.Fprintf(&b, "## %s\n", title)
		var row []string
		var cellType, value string
		inValue := false
		var inline strings.Builder
		_ = walkXML(raw, func(tok xml.Token) {
			switch t := tok.(type) {
			case xml.StartElement:
				switch t.Name.Local {
				case "row":
					row = row[:0]
				case "c":
					cellType, value = "", ""
					inline.Reset()
					for _, a := range t.Attr {
						if a.Name.Local == "t" {
							cellType = a.Value
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
					row = append(row, strings.ReplaceAll(strings.ReplaceAll(cell, "\t", " "), "\n", " "))
				case "row":
					b.WriteString(strings.TrimRight(strings.Join(row, "\t"), "\t"))
					b.WriteByte('\n')
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
		b.WriteByte('\n')
	}
	return Result{Type: "xlsx", Text: b.String(), Pages: len(sheets)}, nil
}

func sheetNames(r *zip.ReadCloser) []string {
	raw, err := readZip(r, "xl/workbook.xml")
	if err != nil {
		return nil
	}
	var names []string
	_ = walkXML(raw, func(tok xml.Token) {
		if t, ok := tok.(xml.StartElement); ok && t.Name.Local == "sheet" {
			for _, a := range t.Attr {
				if a.Name.Local == "name" {
					names = append(names, a.Value)
				}
			}
		}
	})
	return names
}
