package extract

import (
	"strings"
	"unicode/utf8"
)

// ChunkSize is the target chunk length in bytes: small enough for focused
// snippets and embeddings, large enough to keep context together.
const ChunkSize = 1500

// Chunk splits text into pieces of about target bytes, preferring paragraph
// boundaries, then line/word boundaries, and never cutting a UTF-8 rune.
// Empty pieces are dropped and nothing is repeated between chunks.
func Chunk(text string, target int) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	var chunks []string
	var cur strings.Builder

	flush := func() {
		if s := strings.TrimSpace(cur.String()); s != "" {
			chunks = append(chunks, s)
		}
		cur.Reset()
	}

	for _, para := range strings.Split(text, "\n\n") {
		para = strings.TrimSpace(para)
		if para == "" {
			continue
		}
		for _, piece := range splitLong(para, target) {
			if cur.Len() > 0 && cur.Len()+2+len(piece) > target {
				flush()
			}
			if cur.Len() > 0 {
				cur.WriteString("\n\n")
			}
			cur.WriteString(piece)
		}
	}
	flush()
	return chunks
}

// splitLong cuts s into pieces of at most target bytes, at whitespace when
// there is any in the back half of the window, otherwise at a rune boundary.
func splitLong(s string, target int) []string {
	if len(s) <= target {
		return []string{s}
	}
	var out []string
	for len(s) > target {
		cut := target
		if i := strings.LastIndexAny(s[:cut], " \n\t"); i > target/2 {
			cut = i
		} else {
			for cut > 0 && !utf8.RuneStart(s[cut]) {
				cut--
			}
		}
		if cut == 0 { // a single rune wider than target: take it whole
			_, cut = utf8.DecodeRuneInString(s)
		}
		if piece := strings.TrimSpace(s[:cut]); piece != "" {
			out = append(out, piece)
		}
		s = strings.TrimSpace(s[cut:])
	}
	if s != "" {
		out = append(out, s)
	}
	return out
}
