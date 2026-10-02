package window

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/fclaude/nodwyn/internal/log"
)

// Attached images (ADR 22 §3). The log never holds an image's pixels: Build
// names each image by its digest, and whoever sends the request reads the
// bytes from the workspace's attachment store, checking them against that
// digest, or withholds the image with a line that says why. A request's
// hash covers the digest, not the bytes, and the reasons it withheld any
// are recorded with it (llm.req withheld_images), so nodwyn replay --window
// rebuilds the request exactly without reading a file.

// Why an image is withheld from a request (log.ImageGap).
const (
	ImageMissing   = "missing"   // the stored copy is gone
	ImageChanged   = "changed"   // the stored copy is not the image that was attached
	ImageNoVision  = "no-vision" // the model asked takes no images
	ImageElsewhere = "elsewhere" // the request goes to another host than the one the image was confirmed for
)

// ImageRef is an attached image a built window carries.
type ImageRef struct {
	Seq         uint64 // the attach record
	Name        string
	Stored      string // relative to the workspace root
	SHA256      string
	Bytes       int64
	Destination string // the host it may go to
	msg         int    // index in Result.Messages
	about       string
}

func imageRef(seq uint64, msg int, a log.Attach) ImageRef {
	return ImageRef{Seq: seq, Name: a.Name, Stored: a.Stored, SHA256: a.SHA256, Bytes: a.Bytes, Destination: a.Destination, msg: msg, about: imageAbout(a)}
}

// imageAbout says what an image is: "PNG, 1200×800".
func imageAbout(a log.Attach) string {
	format := strings.ToUpper(strings.TrimPrefix(a.MediaType, "image/"))
	if format == "WEBP" {
		format = "WebP"
	}
	if a.Width > 0 && a.Height > 0 {
		return fmt.Sprintf("%s, %d×%d", format, a.Width, a.Height)
	}
	return format
}

// ImageTokens estimates what an image costs a model's context: 85 tokens,
// and 170 for each 512-pixel tile once the image is scaled to fit within
// 2048×2048 and then so its shorter side is at most 768 (the tiling
// chat-completions vision models use; local models differ, so it is an
// estimate, like bytes/3 for text). Unknown dimensions count as the most
// the formula gives, 1,445. The estimate is recorded with the attachment,
// so a window rebuilds with the number it was built with.
func ImageTokens(width, height int) int {
	if width <= 0 || height <= 0 {
		return 85 + 170*8
	}
	if m := max(width, height); m > 2048 {
		width, height = max(1, width*2048/m), max(1, height*2048/m)
	}
	if m := min(width, height); m > 768 {
		width, height = max(1, width*768/m), max(1, height*768/m)
	}
	return 85 + 170*((width+511)/512)*((height+511)/512)
}

// imageTokens is what the window counts for an attached image: the
// estimate its record holds.
func imageTokens(a log.Attach) int {
	if a.Tokens > 0 {
		return a.Tokens
	}
	return ImageTokens(a.Width, a.Height)
}

// ReadImage reads an attached image's stored copy under root and checks it
// is the image that was attached: a regular file inside the attachment
// store, of the recorded size and digest. reason is ImageMissing or
// ImageChanged when it is not.
func ReadImage(root string, ref ImageRef) (data []byte, reason string) {
	rel := filepath.FromSlash(ref.Stored)
	if !filepath.IsLocal(rel) || !strings.HasPrefix(filepath.ToSlash(filepath.Clean(rel)), ".nodwyn/attachments/") {
		return nil, ImageMissing
	}
	path := filepath.Join(root, rel)
	info, err := os.Lstat(path)
	if err != nil {
		return nil, ImageMissing
	}
	if !info.Mode().IsRegular() || info.Size() != ref.Bytes {
		return nil, ImageChanged
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, ImageMissing
	}
	defer f.Close()
	data, err = io.ReadAll(io.LimitReader(f, ref.Bytes+1))
	if err != nil {
		return nil, ImageMissing
	}
	sum := sha256.Sum256(data)
	if int64(len(data)) != ref.Bytes || hex.EncodeToString(sum[:]) != ref.SHA256 {
		return nil, ImageChanged
	}
	return data, ""
}

// SetImage gives the request the bytes of the image attached at seq. They
// are not part of the request's hash.
func (r *Result) SetImage(seq uint64, data []byte) {
	for _, ref := range r.Images {
		if ref.Seq == seq {
			r.Messages[ref.msg].Images[0].Data = data
		}
	}
}

// Withhold replaces each listed image with a line saying why the model
// does not see it, so the request never carries a different image, or an
// image where it may not go, silently. A live request and its replay both
// apply it, from the reasons recorded with the request.
func (r *Result) Withhold(gaps []log.ImageGap) {
	for _, gap := range gaps {
		for i, ref := range r.Images {
			if ref.Seq != gap.Seq {
				continue
			}
			r.Messages[ref.msg].Images = nil
			r.Messages[ref.msg].Content = WithheldText(ref, gap.Reason)
			r.Images = append(r.Images[:i:i], r.Images[i+1:]...)
			break
		}
	}
}

// WithheldText is the line a request carries instead of an image.
func WithheldText(ref ImageRef, reason string) string {
	why := "it is not sent with this request"
	switch reason {
	case ImageMissing:
		why = "its stored copy is missing"
	case ImageChanged:
		why = "its stored copy no longer matches the image that was attached"
	case ImageNoVision:
		why = "the model this request goes to does not take images"
	case ImageElsewhere:
		why = fmt.Sprintf("it was attached to go to %s only", ref.Destination)
	}
	return fmt.Sprintf("[Attached image %q (%s), stored at %s, is not shown: %s.]", ref.Name, ref.about, ref.Stored, why)
}
