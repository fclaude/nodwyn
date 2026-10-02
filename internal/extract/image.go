package extract

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// An image is not extracted: it goes to a model that takes images as it is
// (ADR 22 §3). These bound what nodwyn attaches.
const (
	MaxImage     = 10 << 20 // bytes
	MaxImageSide = 8000     // pixels, either side
)

// Image is what nodwyn knows about an image without decoding its pixels:
// its type, decided by its content, and its size from its header.
type Image struct {
	MediaType string // image/png, image/jpeg, image/gif or image/webp
	Format    string // PNG, JPEG, GIF or WebP
	Width     int
	Height    int
}

// ErrImage marks a file that is an image nodwyn cannot attach: another
// format, too large, damaged, or not what its name says.
var ErrImage = errors.New("extract: cannot attach this image")

// imageFormats are the formats nodwyn sends, by the extensions that name
// them.
var imageFormats = map[string]string{".png": "PNG", ".jpg": "JPEG", ".jpeg": "JPEG", ".gif": "GIF", ".webp": "WebP"}

var mediaTypes = map[string]string{"PNG": "image/png", "JPEG": "image/jpeg", "GIF": "image/gif", "WebP": "image/webp"}

// IsImage reports whether the file at path is to be attached as an image:
// its name says it is one, or it has no extension and its content is one
// nodwyn sends.
func IsImage(path, name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	if imageTypes[ext] {
		return true
	}
	if ext != "" {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	head := make([]byte, 16)
	n, _ := io.ReadFull(f, head)
	return sniff(head[:n]) != ""
}

// ReadImage reads the image at path whole and checks it: one of the
// formats nodwyn sends, by its content; the format its name (when it has
// an extension) says; at most MaxImage bytes and MaxImageSide pixels a
// side; and a header that gives its size. The bytes it returns are the
// ones it checked, so what is attached is what was checked.
func ReadImage(path, name string) ([]byte, Image, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, Image{}, err
	}
	if !info.Mode().IsRegular() {
		return nil, Image{}, fmt.Errorf("extract: %s is not a regular file", name)
	}
	ext := strings.ToLower(filepath.Ext(name))
	named, ok := imageFormats[ext]
	if ext != "" && !ok {
		return nil, Image{}, fmt.Errorf("%w: %s is a %s image; nodwyn sends PNG, JPEG, GIF and WebP, so convert it to one of those first", ErrImage, name, strings.ToUpper(strings.TrimPrefix(ext, ".")))
	}
	if info.Size() > MaxImage {
		return nil, Image{}, fmt.Errorf("%w: %s is larger than an image may be (at most %d MB); scale it down or compress it first", ErrImage, name, MaxImage>>20)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, Image{}, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxImage+1))
	if err != nil {
		return nil, Image{}, err
	}
	if len(data) > MaxImage {
		return nil, Image{}, fmt.Errorf("%w: %s grew past %d MB while it was read", ErrImage, name, MaxImage>>20)
	}
	img, err := CheckImage(data, name, named)
	return data, img, err
}

// CheckImage checks an image's bytes: see ReadImage. named is the format its
// name says, or "" when it has no extension.
func CheckImage(data []byte, name, named string) (Image, error) {
	format := sniff(data)
	switch {
	case format == "" && named != "":
		return Image{}, fmt.Errorf("%w: %s is not a %s image: its content is not one", ErrImage, name, named)
	case format == "":
		return Image{}, fmt.Errorf("%w: %s is not a PNG, JPEG, GIF or WebP image", ErrImage, name)
	case named != "" && format != named:
		return Image{}, fmt.Errorf("%w: %s is not a %s image: its content is %s; rename it to match", ErrImage, name, named, format)
	}
	w, h, ok := dimensions(format, data)
	if !ok || w <= 0 || h <= 0 {
		return Image{}, fmt.Errorf("%w: %s does not read as a %s image: its header is damaged", ErrImage, name, format)
	}
	if w > MaxImageSide || h > MaxImageSide {
		return Image{}, fmt.Errorf("%w: %s is %d×%d pixels; an image is at most %d pixels on either side, so scale it down first", ErrImage, name, w, h, MaxImageSide)
	}
	return Image{MediaType: mediaTypes[format], Format: format, Width: w, Height: h}, nil
}

// sniff names an image format by its signature, or "".
func sniff(b []byte) string {
	switch {
	case bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")):
		return "PNG"
	case bytes.HasPrefix(b, []byte{0xff, 0xd8, 0xff}):
		return "JPEG"
	case bytes.HasPrefix(b, []byte("GIF87a")), bytes.HasPrefix(b, []byte("GIF89a")):
		return "GIF"
	case len(b) >= 16 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return "WebP"
	}
	return ""
}

// dimensions reads an image's width and height from its header.
func dimensions(format string, b []byte) (int, int, bool) {
	switch format {
	case "PNG":
		// The signature, then IHDR: length, type, width, height.
		if len(b) < 24 || string(b[12:16]) != "IHDR" {
			return 0, 0, false
		}
		return int(binary.BigEndian.Uint32(b[16:20])), int(binary.BigEndian.Uint32(b[20:24])), true
	case "GIF":
		// The logical screen descriptor follows the signature.
		if len(b) < 10 {
			return 0, 0, false
		}
		return int(binary.LittleEndian.Uint16(b[6:8])), int(binary.LittleEndian.Uint16(b[8:10])), true
	case "JPEG":
		return jpegSize(b)
	case "WebP":
		return webpSize(b)
	}
	return 0, 0, false
}

// jpegSize walks a JPEG's marker segments to its start of frame.
func jpegSize(b []byte) (int, int, bool) {
	i := 2 // past SOI
	for i+4 <= len(b) {
		if b[i] != 0xff {
			return 0, 0, false
		}
		marker := b[i+1]
		if marker == 0xff { // fill byte
			i++
			continue
		}
		if marker == 0x01 || (marker >= 0xd0 && marker <= 0xd7) { // no length
			i += 2
			continue
		}
		if marker == 0xd9 || marker == 0xda { // end of image, start of scan: no frame header first
			return 0, 0, false
		}
		length := int(binary.BigEndian.Uint16(b[i+2 : i+4]))
		if length < 2 {
			return 0, 0, false
		}
		// Start of frame: every SOFn but DHT (C4), JPG (C8) and DAC (CC).
		if marker >= 0xc0 && marker <= 0xcf && marker != 0xc4 && marker != 0xc8 && marker != 0xcc {
			if i+9 > len(b) {
				return 0, 0, false
			}
			return int(binary.BigEndian.Uint16(b[i+7 : i+9])), int(binary.BigEndian.Uint16(b[i+5 : i+7])), true
		}
		i += 2 + length
	}
	return 0, 0, false
}

// webpSize reads the canvas size from a WebP's first chunk: VP8X (extended),
// VP8L (lossless) or VP8 (lossy).
func webpSize(b []byte) (int, int, bool) {
	switch string(b[12:16]) { // sniff saw 16 bytes
	case "VP8X":
		if len(b) < 30 {
			return 0, 0, false
		}
		w := int(b[24]) | int(b[25])<<8 | int(b[26])<<16
		h := int(b[27]) | int(b[28])<<8 | int(b[29])<<16
		return w + 1, h + 1, true
	case "VP8L":
		if len(b) < 25 || b[20] != 0x2f {
			return 0, 0, false
		}
		bits := binary.LittleEndian.Uint32(b[21:25])
		return int(bits&0x3fff) + 1, int(bits>>14&0x3fff) + 1, true
	case "VP8 ":
		if len(b) < 30 || b[23] != 0x9d || b[24] != 0x01 || b[25] != 0x2a {
			return 0, 0, false
		}
		return int(binary.LittleEndian.Uint16(b[26:28]) & 0x3fff), int(binary.LittleEndian.Uint16(b[28:30]) & 0x3fff), true
	}
	return 0, 0, false
}
