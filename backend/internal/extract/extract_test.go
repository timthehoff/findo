package extract

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

func extractString(t *testing.T, ext, content string, maxText int64) (string, error) {
	t.Helper()
	r := bytes.NewReader([]byte(content))
	return Extract(ext, r, int64(len(content)), maxText)
}

func TestExtractPlainText(t *testing.T) {
	got, err := extractString(t, "txt", "hello\nworld\n", 1<<20)
	if err != nil || got != "hello\nworld\n" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestExtractStripsBOM(t *testing.T) {
	got, err := extractString(t, "md", "\xef\xbb\xbf# Title", 1<<20)
	if err != nil || got != "# Title" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestExtractStopsAtTheCap(t *testing.T) {
	got, err := extractString(t, "txt", strings.Repeat("a", 1000), 100)
	if err != nil || len(got) != 100 {
		t.Fatalf("len = %d, err = %v; want exactly the cap", len(got), err)
	}
}

func TestExtractCapNeverSplitsARune(t *testing.T) {
	// "é" is two bytes, so a cap of 5 lands in the middle of the third one.
	got, err := extractString(t, "txt", "ééé", 5)
	if err != nil || got != "éé" || !utf8.ValidString(got) {
		t.Fatalf("got %q, %v; want the two whole runes", got, err)
	}
	// A cap landing exactly after a complete rune keeps it.
	got, err = extractString(t, "txt", "ééé", 4)
	if err != nil || got != "éé" {
		t.Fatalf("cap on a boundary: got %q, %v", got, err)
	}
}

func TestExtractRejectsBinary(t *testing.T) {
	for name, content := range map[string]string{
		"NUL byte":          "text\x00more",
		"mostly invalid":    strings.Repeat("\xff\xfe\xfd", 100),
		"png header in txt": "\x89PNG\r\n\x1a\n\x00\x00",
	} {
		if _, err := extractString(t, "txt", content, 1<<20); !errors.Is(err, ErrBinary) {
			t.Errorf("%s: got %v, want ErrBinary", name, err)
		}
	}
}

func TestExtractToleratesAStrayInvalidByte(t *testing.T) {
	content := strings.Repeat("plain text ", 50) + "\xe9" + " more text"
	got, err := extractString(t, "txt", content, 1<<20)
	if err != nil || !utf8.ValidString(got) || !strings.Contains(got, "more text") {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestExtractUnsupportedTypes(t *testing.T) {
	for _, ext := range []string{"pdf", "doc", "jpg", "nope", ""} {
		if _, err := extractString(t, ext, "x", 100); !errors.Is(err, ErrUnsupported) {
			t.Errorf("%q: got %v, want ErrUnsupported", ext, err)
		}
	}
}

func TestSupportedCoversTextAndCode(t *testing.T) {
	got := map[string]bool{}
	for _, e := range Supported() {
		got[e] = true
	}
	for _, want := range []string{"txt", "md", "go", "py", "yaml", "json", "csv", "sh"} {
		if !got[want] {
			t.Errorf("Supported() is missing %q", want)
		}
	}
	for _, not := range []string{"pdf", "doc", "jpg", "html"} {
		if got[not] {
			t.Errorf("Supported() should not include %q yet", not)
		}
	}
}

func TestChunkPacksParagraphsUnderTheTarget(t *testing.T) {
	text := strings.Repeat("word ", 30) + "\n\n" + strings.Repeat("more ", 30) + "\n\n" + strings.Repeat("last ", 30)
	chunks := Chunk(text, 400)
	if len(chunks) < 2 {
		t.Fatalf("expected several chunks, got %d", len(chunks))
	}
	for i, c := range chunks {
		if len(c) > 400 || strings.TrimSpace(c) != c || c == "" {
			t.Errorf("chunk %d is malformed (len %d): %q", i, len(c), c)
		}
	}
	// Nothing lost, nothing repeated.
	if strings.Join(strings.Fields(strings.Join(chunks, " ")), " ") != strings.Join(strings.Fields(text), " ") {
		t.Fatal("chunks don't reassemble to the original text")
	}
}

func TestChunkSplitsAnOversizedParagraphAtWords(t *testing.T) {
	text := strings.Repeat("alpha beta gamma ", 200)
	chunks := Chunk(text, 300)
	for _, c := range chunks {
		if len(c) > 300 {
			t.Fatalf("chunk over target: %d", len(c))
		}
		if !strings.HasSuffix(c, "gamma") && !strings.HasSuffix(c, "beta") && !strings.HasSuffix(c, "alpha") {
			t.Fatalf("chunk was cut inside a word: ...%q", c[len(c)-6:])
		}
	}
}

func TestChunkHardSplitsWithoutWhitespaceOnRuneBoundaries(t *testing.T) {
	text := strings.Repeat("日本語", 200) // no spaces, 3 bytes per rune
	chunks := Chunk(text, 100)
	if len(chunks) < 2 {
		t.Fatal("expected a split")
	}
	for _, c := range chunks {
		if !utf8.ValidString(c) || len(c) > 100 {
			t.Fatalf("bad chunk (len %d, valid %v)", len(c), utf8.ValidString(c))
		}
	}
	if strings.Join(chunks, "") != text {
		t.Fatal("hard split lost or duplicated text")
	}
}

func TestChunkEdgeCases(t *testing.T) {
	if got := Chunk("", 100); len(got) != 0 {
		t.Errorf("empty text: %v", got)
	}
	if got := Chunk("  \n\n \r\n  ", 100); len(got) != 0 {
		t.Errorf("whitespace only: %v", got)
	}
	if got := Chunk("one\r\n\r\ntwo", 100); len(got) != 1 || got[0] != "one\n\ntwo" {
		t.Errorf("CRLF paragraphs: %q", got)
	}
}

// The highlight markers are control characters, so none may survive into
// stored text; ordinary whitespace must.
func TestExtractStripsControlCharacters(t *testing.T) {
	got, err := extractString(t, "txt", "a\x02b\x03c\x01d\x7fe\tf\ng\rh", 1<<20)
	if err != nil || got != "abcde\tf\ng\rh" {
		t.Fatalf("got %q, %v", got, err)
	}
}
