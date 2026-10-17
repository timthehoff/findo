package filetype

import (
	"strings"
	"testing"
)

func TestLookup(t *testing.T) {
	tests := []struct {
		name       string
		wantMIME   string
		wantKind   string
		wantInline bool
	}{
		{"Report.PDF", "application/pdf", KindPDF, true},
		{"photo.jpeg", "image/jpeg", KindImg, true},
		{"notes.md", "text/plain; charset=utf-8", KindTxt, true},
		{"data.csv", "text/plain; charset=utf-8", KindXLS, true},
		{"clip.mp4", "video/mp4", KindVid, true},
		{"song.mp3", "audio/mpeg", KindAud, true},
		{"voice.m4a", "audio/mp4", KindAud, true},
		{"pic.avif", "image/avif", KindImg, true},
		{"app.log", "text/plain; charset=utf-8", KindTxt, true},
		{"budget.xlsx", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", KindXLS, false},
		{"budget.numbers", "application/vnd.apple.numbers", KindXLS, false},
		{"archive.tar.gz", "application/gzip", KindZip, false},
		{"page.html", "text/html; charset=utf-8", KindFile, false},
		{"logo.svg", "image/svg+xml", KindFile, false},
		{"README", octet, KindFile, false},
		{".gitignore", octet, KindFile, false},
		{"weird.xyz", octet, KindFile, false},
	}
	for _, tt := range tests {
		got := Lookup(tt.name)
		if got.MIME != tt.wantMIME || got.Kind != tt.wantKind || got.Inline != tt.wantInline {
			t.Errorf("Lookup(%q) = %+v, want mime=%q kind=%q inline=%v", tt.name, got, tt.wantMIME, tt.wantKind, tt.wantInline)
		}
	}
}

// Active content must never be served inline from the dashboard's origin.
func TestActiveContentIsNeverInline(t *testing.T) {
	for _, ext := range []string{"html", "htm", "svg"} {
		info, ok := ByExt(ext)
		if !ok || info.Inline {
			t.Errorf("%s = %+v (known=%v); must be known and not inline", ext, info, ok)
		}
	}
	for ext, info := range All() {
		if info.Inline && (strings.Contains(info.MIME, "html") || strings.Contains(info.MIME, "svg")) {
			t.Errorf("%s is inline as %s", ext, info.MIME)
		}
	}
}

func TestEveryEntryIsWellFormed(t *testing.T) {
	kinds := map[string]bool{KindPDF: true, KindDoc: true, KindXLS: true, KindPPT: true, KindImg: true, KindVid: true, KindAud: true, KindZip: true, KindTxt: true, KindFile: true}
	for ext, info := range All() {
		if ext != strings.ToLower(ext) || strings.HasPrefix(ext, ".") {
			t.Errorf("extension key %q must be lowercase without a dot", ext)
		}
		if info.MIME == "" || !kinds[info.Kind] {
			t.Errorf("%s has an incomplete entry: %+v", ext, info)
		}
	}
}

func TestAllReturnsACopy(t *testing.T) {
	all := All()
	delete(all, "pdf")
	if _, ok := ByExt("pdf"); !ok {
		t.Fatal("mutating All()'s result changed the registry")
	}
}
