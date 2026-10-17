package extract

import (
	"bytes"
	"io"
	"strings"
	"unicode/utf8"
)

// extractText handles plain text, markup and source code. It reads at most
// maxText bytes (plus a few to finish a UTF-8 sequence), refuses anything with
// a NUL byte or mostly invalid UTF-8 (a binary file with a text extension),
// and tolerates the odd stray byte.
func extractText(r io.ReaderAt, size, maxText int64) (string, error) {
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

	if !utf8.Valid(buf) {
		bad, total := 0, 0
		for i := 0; i < len(buf); {
			rn, w := utf8.DecodeRune(buf[i:])
			if rn == utf8.RuneError && w == 1 {
				bad++
			}
			total++
			i += w
		}
		if bad*20 > total { // over 5% invalid: not UTF-8 text
			return "", ErrBinary
		}
		return strings.ToValidUTF8(string(buf), "�"), nil
	}
	return string(buf), nil
}
