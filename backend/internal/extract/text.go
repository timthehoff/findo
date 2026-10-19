package extract

import (
	"bytes"
	"context"
	"io"
	"strings"
	"unicode/utf8"
)

// extractText handles plain text, markup and source code. It reads at most
// maxText bytes (plus a few to finish a UTF-8 sequence), refuses anything with
// a NUL byte or mostly invalid UTF-8 (a binary file with a text extension),
// and tolerates the odd stray byte.
func extractText(_ context.Context, r io.ReaderAt, size, maxText int64) (string, error) {
	n := size
	if n > maxText+utf8.UTFMax {
		n = maxText + utf8.UTFMax
	}
	buf := make([]byte, n)
	read, err := r.ReadAt(buf, 0)
	if err != nil && err != io.EOF {
		return "", err
	}
	buf = buf[:read]

	if bytes.IndexByte(buf, 0) >= 0 {
		return "", ErrBinary
	}
	buf = bytes.TrimPrefix(buf, []byte("\xef\xbb\xbf")) // UTF-8 BOM

	if int64(len(buf)) > maxText {
		buf = buf[:maxText]
		// Don't end mid-rune: drop trailing bytes of a sequence the cap cut.
		for i := 0; i < utf8.UTFMax-1 && len(buf) > 0; i++ {
			if r, w := utf8.DecodeLastRune(buf); r != utf8.RuneError || w != 1 {
				break
			}
			buf = buf[:len(buf)-1]
		}
	}

	text, err := decodeUTF8(buf)
	if err != nil {
		return "", err
	}
	return stripControls(text), nil
}

// decodeUTF8 returns buf as a valid UTF-8 string, replacing the odd invalid
// byte, or ErrBinary if more than 5% of it is invalid (not UTF-8 text).
func decodeUTF8(buf []byte) (string, error) {
	if utf8.Valid(buf) {
		return string(buf), nil
	}
	bad, total := 0, 0
	for i := 0; i < len(buf); {
		rn, w := utf8.DecodeRune(buf[i:])
		if rn == utf8.RuneError && w == 1 {
			bad++
		}
		total++
		i += w
	}
	if bad*20 > total {
		return "", ErrBinary
	}
	return strings.ToValidUTF8(string(buf), "�"), nil
}

// stripControls keeps tab, newline and carriage return and drops other
// control characters: they're noise in search results, and the highlight
// markers (see index.MarkStart) are control characters that must never occur
// in stored text.
func stripControls(s string) string {
	return strings.Map(func(r rune) rune {
		if (r < 0x20 && r != '\t' && r != '\n' && r != '\r') || r == 0x7f {
			return -1
		}
		return r
	}, s)
}
