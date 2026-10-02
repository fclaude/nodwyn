package extract

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pngOf encodes a w×h image of one colour.
func pngOf(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = 0x80
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// webp builds a WebP's RIFF header and first chunk, which is all nodwyn reads.
func webp(chunk string, payload []byte) []byte {
	var b bytes.Buffer
	b.WriteString("RIFF")
	binary.Write(&b, binary.LittleEndian, uint32(4+8+len(payload)))
	b.WriteString("WEBP" + chunk)
	binary.Write(&b, binary.LittleEndian, uint32(len(payload)))
	b.Write(payload)
	return b.Bytes()
}

func write(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestImageFormatsAndSizes(t *testing.T) {
	var jpg, gf bytes.Buffer
	jpeg.Encode(&jpg, image.NewGray(image.Rect(0, 0, 40, 30)), nil)
	gif.Encode(&gf, image.NewPaletted(image.Rect(0, 0, 10, 20), color.Palette{color.Black, color.White}), nil)
	vp8x := make([]byte, 10)
	vp8x[4], vp8x[5] = 0x7f, 0x02 // width-1 = 639
	vp8x[7], vp8x[8] = 0xdf, 0x01 // height-1 = 479
	vp8l := []byte{0x2f, 0, 0, 0, 0}
	binary.LittleEndian.PutUint32(vp8l[1:], uint32(33-1)|uint32(44-1)<<14)
	vp8 := []byte{0, 0, 0, 0x9d, 0x01, 0x2a, 100, 0, 50, 0}
	for _, c := range []struct {
		name, media string
		data        []byte
		w, h        int
	}{
		{"a.png", "image/png", pngOf(t, 1200, 800), 1200, 800},
		{"b.jpg", "image/jpeg", jpg.Bytes(), 40, 30},
		{"b.JPEG", "image/jpeg", jpg.Bytes(), 40, 30},
		{"c.gif", "image/gif", gf.Bytes(), 10, 20},
		{"d.webp", "image/webp", webp("VP8X", vp8x), 640, 480},
		{"e.webp", "image/webp", webp("VP8L", vp8l), 33, 44},
		{"f.webp", "image/webp", webp("VP8 ", vp8), 100, 50},
		{"screenshot", "image/png", pngOf(t, 3, 2), 3, 2}, // no extension: the content decides
	} {
		path := write(t, c.name, c.data)
		if !IsImage(path, c.name) {
			t.Fatalf("%s is not taken for an image", c.name)
		}
		data, img, err := ReadImage(path, c.name)
		if err != nil || img.MediaType != c.media || img.Width != c.w || img.Height != c.h || !bytes.Equal(data, c.data) {
			t.Fatalf("%s: %+v %v", c.name, img, err)
		}
	}
}

func TestImageIsDecidedByContent(t *testing.T) {
	var jpg bytes.Buffer
	jpeg.Encode(&jpg, image.NewGray(image.Rect(0, 0, 4, 4)), nil)
	for _, c := range []struct {
		name string
		data []byte
		want string
	}{
		{"photo.png", jpg.Bytes(), "is not a PNG image: its content is JPEG"},
		{"photo.png", []byte("just text, not an image"), "is not a PNG image"},
		{"photo.png", []byte("\x89PNG"), "is not a PNG image"},
		{"photo.png", append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 20)...), "header is damaged"},
		{"photo.jpg", []byte{0xff, 0xd8, 0xff, 0xd9}, "header is damaged"},
		{"photo.heic", []byte("anything"), "convert it"},
		{"scan.bmp", []byte("BM...."), "convert it"},
	} {
		path := write(t, c.name, c.data)
		if !IsImage(path, c.name) {
			t.Fatalf("%s is not taken for an image", c.name)
		}
		if _, _, err := ReadImage(path, c.name); !errors.Is(err, ErrImage) || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%s (%q): %v", c.name, c.data[:min(8, len(c.data))], err)
		}
	}
	// A text file whose content is an image is still a text file: its name
	// says so, and as text it is refused.
	if path := write(t, "notes.txt", pngOf(t, 2, 2)); IsImage(path, "notes.txt") {
		t.Fatal("a .txt taken for an image")
	}
	if path := write(t, "README", []byte("plain words")); IsImage(path, "README") {
		t.Fatal("an extensionless text file taken for an image")
	}
}

func TestImageCaps(t *testing.T) {
	big := write(t, "huge.png", pngOf(t, 2, 2))
	if err := os.Truncate(big, MaxImage+1); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadImage(big, "huge.png"); !errors.Is(err, ErrImage) || !strings.Contains(err.Error(), "at most 10 MB") {
		t.Fatalf("size cap: %v", err)
	}
	wide := pngOf(t, 2, 2)
	binary.BigEndian.PutUint32(wide[16:20], MaxImageSide+1) // IHDR width
	if _, _, err := ReadImage(write(t, "wide.png", wide), "wide.png"); !errors.Is(err, ErrImage) || !strings.Contains(err.Error(), "8001×2 pixels") || !strings.Contains(err.Error(), "at most 8000") {
		t.Fatalf("side cap: %v", err)
	}
	if _, _, err := ReadImage(write(t, "edge.png", pngOf(t, MaxImageSide, 1)), "edge.png"); err != nil {
		t.Fatalf("an image at the cap: %v", err)
	}
}
