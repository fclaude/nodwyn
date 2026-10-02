package extract

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

// AllText returns every text a file's content carries, for a secret scan
// before the file leaves the machine (W5). It is not what a reader of the
// document sees, which is File's job, but whatever the bytes sent could
// disclose; it reads the bytes given, so what is scanned is what is sent.
// A text file is its bytes. A .docx or .odt is every XML part of its
// package (the body, headers, footers, notes, comments, properties,
// relationships), each as its markup and again as its character data
// joined by paragraph, so a key split across text runs is whole again.
// Images and fonts in the package are not read: pixels and glyphs are not
// text a scanner can check, the limit ADR 22 §3 accepts for attachments. A
// package holding anything else (an embedded file, a macro project) is
// refused, since what it holds cannot be checked. name, the file's name,
// decides its type.
func AllText(content []byte, name string) (string, error) {
	if len(content) > MaxInput {
		return "", fmt.Errorf("extract: %s is %d MB; at most %d MB", name, len(content)>>20, MaxInput>>20)
	}
	ext := strings.ToLower(filepath.Ext(name))
	switch {
	case ext == ".docx" || ext == ".odt":
		return packageText(content, name)
	case textTypes[ext]:
		return string(content), nil
	}
	return "", fmt.Errorf("%w: %s (%s)", ErrUnsupported, name, ext)
}

// packageParts says what AllText does with a part of an Office package, by
// its extension: scan it as text, or pass over it (images and fonts).
var packageParts = map[string]bool{
	".xml": true, ".rels": true, ".rdf": true, ".vml": true, ".svg": true, "": true, // "" is ODF's mimetype
	".png": false, ".jpg": false, ".jpeg": false, ".gif": false, ".bmp": false, ".tif": false, ".tiff": false,
	".emf": false, ".wmf": false, ".webp": false, ".odttf": false, ".ttf": false, ".otf": false, ".fntdata": false,
}

func packageText(content []byte, name string) (string, error) {
	r, err := zip.NewReader(bytes.NewReader(content), int64(len(content)))
	if err != nil {
		return "", fmt.Errorf("extract: %s is not a valid Office file: %w", name, err)
	}
	if err := unpackBound(r); err != nil {
		return "", err
	}
	var b strings.Builder
	for _, f := range r.File {
		if strings.HasSuffix(f.Name, "/") {
			continue
		}
		scan, known := packageParts[strings.ToLower(filepath.Ext(f.Name))]
		if !known {
			return "", fmt.Errorf("extract: %s holds %s, which is not text nodwyn can check for secrets: remove it from the document first", name, f.Name)
		}
		if !scan {
			continue
		}
		raw, err := readPart(f)
		if err != nil {
			return "", fmt.Errorf("extract: %s: %s: %w", name, f.Name, err)
		}
		b.Write(raw)
		b.WriteByte('\n')
		// Character data again, joined within a paragraph: Word splits a
		// line into runs at every change of formatting, or of nothing at all.
		_ = walkXML(raw, func(tok xml.Token) {
			switch t := tok.(type) {
			case xml.CharData:
				b.Write(t)
			case xml.EndElement:
				if t.Name.Local == "p" {
					b.WriteByte('\n')
				}
			}
		})
		b.WriteByte('\n')
	}
	return b.String(), nil
}

func readPart(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(io.LimitReader(rc, maxUnpacked))
}
