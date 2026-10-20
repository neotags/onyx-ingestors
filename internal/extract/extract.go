// Package extract converts attachment bytes (pdf, docx, pptx, xlsx, drawio,
// plain text …) into markdown-ish plain text suitable for ingestion. The
// structured office formats go through anymd (pure Go, no cgo); lightweight
// formats (drawio XML, text-ish files) are handled with the standard library
// so they neither pay anymd's detection cost nor its surface area.
package extract

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/muthuishere/anymd"
)

// ErrSkip marks an attachment that cannot yield useful text (scanned image
// PDFs, unsupported types). It is not a run failure: callers log and move on.
var ErrSkip = errors.New("no extractable text")

// DefaultTypes lists the extensions enabled when the operator does not
// configure CONFLUENCE_ATTACHMENT_TYPES. Binary images (png/jpg/…)
// deliberately stay out: without a vision model they carry no text worth
// indexing, and they dominate the attachment count of a typical space.
var DefaultTypes = []string{
	"pdf", "docx", "pptx", "xlsx", "csv", "md", "txt", "log",
	"json", "yaml", "yml", "drawio",
}

// ExtensionOf returns the lowercase, dot-less extension ("pdf") of name.
func ExtensionOf(name string) string {
	return strings.ToLower(strings.TrimPrefix(filepath.Ext(name), "."))
}

// Truncate caps text at maxChars runes, appending a marker. maxChars<=0
// leaves the text alone.
func Truncate(text string, maxChars int) string {
	if maxChars <= 0 {
		return text
	}
	runes := []rune(text)
	if len(runes) <= maxChars {
		return text
	}
	return string(runes[:maxChars]) + "\n\n[… truncated — exceeded character cap]"
}

// imageTypes are declined up front: without a vision model the only text
// available is EXIF metadata, which is indexing noise.
var imageTypes = map[string]bool{
	"png": true, "jpg": true, "jpeg": true, "gif": true,
	"webp": true, "tiff": true, "bmp": true, "svg": true,
}

// Convert dispatches on the file extension: images are declined, drawio and
// text-ish formats use the small standard-library converters, everything
// else is handed to anymd. A type not covered returns ErrSkip.
func Convert(name string, data []byte) (string, error) {
	ext := ExtensionOf(name)
	switch {
	case imageTypes[ext]:
		return "", fmt.Errorf("%w: image attachment", ErrSkip)
	case ext == "drawio":
		return drawioToText(data)
	case textTypes[ext]:
		return textToText(data), nil
	default:
		return anymdConvert(name, data)
	}
}

// textTypes pass through unchanged after charset normalization.
var textTypes = map[string]bool{
	"csv": true, "md": true, "txt": true, "log": true,
	"json": true, "yaml": true, "yml": true,
}

func anymdConvert(name string, data []byte) (string, error) {
	res, err := anymd.ConvertBytes(data, anymd.StreamInfo{
		FileName:  name,
		Extension: "." + ExtensionOf(name),
	})
	switch {
	case err == nil:
		return res.Markdown, nil
	case errors.Is(err, anymd.ErrNoTextLayer):
		return "", fmt.Errorf("%w: %s", ErrSkip, err)
	default:
		var unsupported *anymd.UnsupportedError
		if errors.As(err, &unsupported) {
			return "", fmt.Errorf("%w: %s", ErrSkip, err)
		}
		return "", fmt.Errorf("anymd convert: %w", err)
	}
}

// textToText normalizes the encoding quirks of ad-hoc text attachments:
// a UTF-8 BOM and invalid UTF-8 bytes are both common in exported logs.
func textToText(data []byte) string {
	s := string(data)
	s = strings.TrimPrefix(s, "\uFEFF")
	return strings.ToValidUTF8(s, "")
}
