package filetype

import (
	"sort"
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
		{"track.FLAC", "audio/flac", KindAud, true},
		{"track.aac", "audio/aac", KindAud, true},
		{"track.ogg", "audio/ogg", KindAud, false},
		{"track.aiff", "audio/aiff", KindAud, false},
		{"track.aifc", "audio/aiff", KindAud, false},
		{"track.caf", "audio/x-caf", KindAud, false},
		{"bought.m4p", "audio/mp4", KindAud, false},
		{"ring.m4r", "audio/mp4", KindAud, false},
		{"old.au", "audio/basic", KindAud, false},
		{"tune.mid", "audio/midi", KindAud, false},
		{"tune.MIDI", "audio/midi", KindAud, false},
		{"pic.avif", "image/avif", KindImg, true},
		{"app.log", "text/plain; charset=utf-8", KindTxt, true},
		{"budget.xlsx", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", KindXLS, false},
		{"budget.numbers", "application/vnd.apple.numbers", KindXLS, false},
		{"archive.tar.gz", "application/gzip", KindZip, false},
		{"page.html", "text/html; charset=utf-8", KindFile, false},
		{"logo.svg", "image/svg+xml", KindImg, false},
		{"IMG_0001.HEIC", "image/heic", KindImg, false},
		{"scan.tiff", "image/tiff", KindImg, false},
		{"shot.CR2", "image/x-canon-cr2", KindImg, false},
		{"mockup.psd", "image/vnd.adobe.photoshop", KindImg, false},
		{"icon.bmp", "image/bmp", KindImg, true},
		{"anim.apng", "image/apng", KindImg, true},
		{"movie.wmv", "video/x-ms-wmv", KindVid, false},
		{"movie.WEBM", "video/webm", KindVid, true},
		{"clip.3gp", "video/3gpp", KindVid, false},
		{"cam.m2ts", "video/mp2t", KindVid, false},
		{"main.go", "text/plain; charset=utf-8", KindTxt, true},
		{"App.TSX", "text/plain; charset=utf-8", KindTxt, true},
		{"style.scss", "text/plain; charset=utf-8", KindTxt, true},
		{"deploy.sh", "text/plain; charset=utf-8", KindTxt, true},
		{"config.yaml", "text/plain; charset=utf-8", KindTxt, true},
		{"schema.sql", "text/plain; charset=utf-8", KindTxt, true},
		{"video.ts", octet, KindFile, false},
		{"README", octet, KindFile, false},
		{".DS_Store", octet, KindFile, false},
		{".gitignore", "text/plain; charset=utf-8", KindTxt, true},
		{"values.yml", "text/plain; charset=utf-8", KindTxt, true},
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

// Code is served as text/plain so it displays, and the registry panics on a
// repeated extension: importing the package at all proves there are none.
func TestSourceCodeIsPlainTextInline(t *testing.T) {
	for _, ext := range []string{"py", "js", "go", "rs", "c", "cpp", "java", "rb", "php", "sh", "css", "yml", "toml"} {
		info, ok := ByExt(ext)
		if !ok || info.MIME != "text/plain; charset=utf-8" || !info.Inline || info.Kind != KindTxt {
			t.Errorf("%s = %+v (known=%v)", ext, info, ok)
		}
	}
}

func TestAddPanicsOnDuplicateExtension(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("add of an existing extension did not panic")
		}
	}()
	add("text/plain", KindTxt, true, "pdf")
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

func TestExtractorAssignments(t *testing.T) {
	exts := ExtsWithExtractor(ExtText)
	if !sort.StringsAreSorted(exts) {
		t.Error("ExtsWithExtractor must return sorted extensions")
	}
	have := map[string]bool{}
	for _, e := range exts {
		have[e] = true
	}
	for _, want := range []string{"txt", "md", "json", "csv", "go", "py", "yaml", "sh"} {
		if !have[want] {
			t.Errorf("%s should use the text extractor", want)
		}
	}
	// Binary formats and active markup have no extractor (yet).
	for _, not := range []string{"pdf", "docx", "jpg", "mp4", "zip", "html", "svg"} {
		if have[not] {
			t.Errorf("%s must not use the text extractor", not)
		}
	}
	if info, _ := ByExt("go"); info.Extractor != ExtText {
		t.Errorf("go extractor = %q", info.Extractor)
	}
	if len(ExtsWithExtractor("nonexistent")) != 0 {
		t.Error("an unknown extractor name should match nothing")
	}
}
