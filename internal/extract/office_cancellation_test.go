package extract

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func checkedOfficeFile(t *testing.T, name, entry, xml string) string {
	t.Helper()
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	member, err := archive.Create(entry)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := member.Write([]byte(xml)); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, buffer.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestOfficeExtractionHonoursCancellation(t *testing.T) {
	for _, fixture := range []struct{ name, entry, xml string }{
		{"a.docx", "word/document.xml", "<document><p><t>text</t></p></document>"},
		{"a.pptx", "ppt/slides/slide1.xml", "<slide><p><t>text</t></p></slide>"},
		{"a.xlsx", "xl/worksheets/sheet1.xml", "<worksheet><row><c><v>1</v></c></row></worksheet>"},
	} {
		path := checkedOfficeFile(t, fixture.name, fixture.entry, fixture.xml)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := File(ctx, path, fixture.name); !errors.Is(err, context.Canceled) {
			t.Errorf("%s ignored cancellation: %v", fixture.name, err)
		}
	}
}

// Cancelling after parsing starts must stop the same call, rather than
// only being checked on entry. Counting Err calls makes this deterministic.
type officeCancelContext struct {
	context.Context
	cancel context.CancelFunc
	checks int
}

func (c *officeCancelContext) Err() error {
	c.checks++
	if c.checks == 8 {
		c.cancel()
	}
	return c.Context.Err()
}

func TestOfficeExtractionStopsDuringParsing(t *testing.T) {
	for _, fixture := range []struct{ name, entry, xml string }{
		{"a.docx", "word/document.xml", "<document><p><t>text</t><t>more</t></p></document>"},
		{"a.pptx", "ppt/slides/slide1.xml", "<slide><p><t>text</t><t>more</t></p></slide>"},
		{"a.xlsx", "xl/worksheets/sheet1.xml", "<worksheet><row><c><v>1</v></c><c><v>2</v></c></row></worksheet>"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			path := checkedOfficeFile(t, fixture.name, fixture.entry, fixture.xml)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			counting := &officeCancelContext{Context: ctx, cancel: cancel}
			if _, err := File(counting, path, fixture.name); !errors.Is(err, context.Canceled) {
				t.Fatalf("parsing ignored cancellation: %v (checks %d)", err, counting.checks)
			}
		})
	}
}
