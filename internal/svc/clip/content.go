package clip

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/gif" // registered for image.Decode in ImageFromBytes
	_ "image/jpeg"
	"image/png"
	"unicode/utf8"
)

// MaxSize is the largest clipboard item (text or PNG bytes) that is read,
// written or transferred. Larger content is rejected rather than truncated.
const MaxSize = 32 << 20

// Kind is the type of clipboard content.
type Kind string

const (
	KindText  Kind = "text"
	KindImage Kind = "image"
)

// MIME types of the two content kinds. Images are always PNG.
const (
	MIMEText  = "text/plain; charset=utf-8"
	MIMEImage = "image/png"
)

var (
	// ErrEmpty means the clipboard holds nothing we can represent.
	ErrEmpty = errors.New("clipboard is empty")
	// ErrTooLarge means the content exceeds MaxSize.
	ErrTooLarge = fmt.Errorf("clipboard content exceeds %d MiB limit", MaxSize>>20)
)

// Content is one clipboard item.
type Content struct {
	Kind Kind
	MIME string
	Data []byte
}

// Text builds text content.
func Text(s string) Content { return Content{Kind: KindText, MIME: MIMEText, Data: []byte(s)} }

// PNG builds image content from PNG bytes, validating them.
func PNG(data []byte) (Content, error) {
	c := Content{Kind: KindImage, MIME: MIMEImage, Data: data}
	return c, c.Validate()
}

// String returns the text of text content ("" for images).
func (c Content) String() string {
	if c.Kind != KindText {
		return ""
	}
	return string(c.Data)
}

// Hash identifies the content: hex SHA-256 over the kind and the data.
func (c Content) Hash() string {
	h := sha256.New()
	h.Write([]byte(c.Kind))
	h.Write([]byte{0})
	h.Write(c.Data)
	return hex.EncodeToString(h.Sum(nil))
}

// Validate checks that c is well-formed and within the size limit. Text must
// be non-empty UTF-8; images must be PNG.
func (c Content) Validate() error {
	if len(c.Data) == 0 {
		return ErrEmpty
	}
	if len(c.Data) > MaxSize {
		return ErrTooLarge
	}
	switch c.Kind {
	case KindText:
		if !utf8.Valid(c.Data) {
			return errors.New("clipboard text is not valid UTF-8")
		}
	case KindImage:
		if !isPNG(c.Data) {
			return errors.New("clipboard image is not a valid PNG")
		}
	default:
		return fmt.Errorf("unknown clipboard kind %q", c.Kind)
	}
	return nil
}

func normalize(c Content) Content {
	switch c.Kind {
	case KindText:
		c.MIME = MIMEText
	case KindImage:
		c.MIME = MIMEImage
	}
	return c
}

var pngMagic = []byte("\x89PNG\r\n\x1a\n")

// isPNG checks the signature and parses the header; it does not decode the
// pixels, so it is cheap and cannot be made to allocate by a huge image.
func isPNG(b []byte) bool {
	if !bytes.HasPrefix(b, pngMagic) {
		return false
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(b))
	return err == nil && cfg.Width > 0 && cfg.Height > 0
}

// maxPixels bounds images we decode for conversion (about 100 megapixels).
const maxPixels = 100 << 20

// ImageFromBytes turns PNG, JPEG or GIF data into PNG content. PNG input is
// kept byte for byte.
func ImageFromBytes(data []byte) (Content, error) {
	if len(data) > MaxSize {
		return Content{}, ErrTooLarge
	}
	if isPNG(data) {
		return PNG(data)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return Content{}, errors.New("not a PNG, JPEG or GIF image")
	}
	if cfg.Width*cfg.Height > maxPixels {
		return Content{}, errors.New("image dimensions too large")
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return Content{}, fmt.Errorf("decode image: %w", err)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return Content{}, err
	}
	return PNG(buf.Bytes())
}
