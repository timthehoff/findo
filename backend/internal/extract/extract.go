// Package extract turns a file's bytes into searchable text. Which extractor
// applies to which extension is the file-type registry's call
// (internal/filetype); this package owns the implementations and the
// chunking that follows them. Extractors are bounded by construction: they
// stop reading at a caller-supplied text cap.
package extract

import (
	"errors"
	"fmt"
	"io"

	"github.com/thoff/findo/backend/internal/filetype"
)

var (
	// ErrUnsupported means no extractor is registered for the extension.
	ErrUnsupported = errors.New("no extractor for this file type")
	// ErrBinary means the file isn't text despite its extension.
	ErrBinary = errors.New("file is not text")
)

// Func reads up to roughly maxText bytes of text from r (size bytes long).
type Func func(r io.ReaderAt, size, maxText int64) (string, error)

var extractors = map[string]Func{
	filetype.ExtText: extractText,
}

// Supported returns the lowercase extensions Extract can handle right now.
func Supported() []string {
	names := make([]string, 0, len(extractors))
	for n := range extractors {
		names = append(names, n)
	}
	return filetype.ExtsWithExtractor(names...)
}

// Extract returns the text of a file with the given lowercase extension.
func Extract(ext string, r io.ReaderAt, size, maxText int64) (string, error) {
	info, ok := filetype.ByExt(ext)
	if !ok || info.Extractor == "" {
		return "", ErrUnsupported
	}
	fn, ok := extractors[info.Extractor]
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrUnsupported, info.Extractor)
	}
	return fn(r, size, maxText)
}
