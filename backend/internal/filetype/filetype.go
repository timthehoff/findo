// Package filetype is the one place Findo decides what a file is, from its
// extension: the Content-Type to serve, whether a browser may render it
// inline, and the kind that picks its color tile in the dashboard. The table
// is explicit rather than derived from the OS MIME database so behavior
// doesn't depend on the host (macOS and a container disagree).
package filetype

import (
	"path"
	"strings"
)

// Kind names a family of file types. The values double as the dashboard's
// tile classes (type-pdf, type-doc, ...), so a new kind needs a color there.
const (
	KindPDF  = "pdf"
	KindDoc  = "doc"
	KindXLS  = "xls"
	KindPPT  = "ppt"
	KindImg  = "img"
	KindVid  = "vid"
	KindAud  = "aud"
	KindZip  = "zip"
	KindTxt  = "txt"
	KindFile = "file" // anything not listed
)

// Info describes one extension.
type Info struct {
	// MIME is the Content-Type served for the file. Types shown inline use
	// text/plain for anything textual, so the browser displays it rather than
	// offering a download or guessing at markup.
	MIME string `json:"mime"`
	Kind string `json:"kind"`
	// Inline is true only for types browsers (Safari and Chromium) render
	// themselves. HTML and SVG are deliberately never inline: rendering
	// NAS-supplied markup on the dashboard's origin would let it script
	// against the API.
	Inline bool `json:"inline"`
}

const (
	textPlain = "text/plain; charset=utf-8"
	octet     = "application/octet-stream"
)

var types = map[string]Info{}

func init() {
	for _, r := range []struct {
		ext, mime, kind string
		inline          bool
	}{
		{"pdf", "application/pdf", KindPDF, true},

		{"doc", "application/msword", KindDoc, false},
		{"docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", KindDoc, false},
		{"pages", "application/vnd.apple.pages", KindDoc, false},
		{"rtf", "application/rtf", KindDoc, false},

		{"xls", "application/vnd.ms-excel", KindXLS, false},
		{"xlsx", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", KindXLS, false},
		{"numbers", "application/vnd.apple.numbers", KindXLS, false},
		{"csv", textPlain, KindXLS, true},

		{"ppt", "application/vnd.ms-powerpoint", KindPPT, false},
		{"pptx", "application/vnd.openxmlformats-officedocument.presentationml.presentation", KindPPT, false},
		{"key", "application/vnd.apple.keynote", KindPPT, false},

		{"jpg", "image/jpeg", KindImg, true},
		{"jpeg", "image/jpeg", KindImg, true},
		{"png", "image/png", KindImg, true},
		{"gif", "image/gif", KindImg, true},
		{"webp", "image/webp", KindImg, true},
		{"heic", "image/heic", KindImg, false},
		{"avif", "image/avif", KindImg, true},
		{"svg", "image/svg+xml", KindFile, false},

		{"mp4", "video/mp4", KindVid, true},
		{"mov", "video/quicktime", KindVid, true},
		{"avi", "video/x-msvideo", KindVid, false},
		{"mkv", "video/x-matroska", KindVid, false},
		{"m4v", "video/mp4", KindVid, true},
		{"webm", "video/webm", KindVid, true},

		// Audio: inline only for what Safari and Chromium both play; the rest
		// (and MIDI, which no browser synthesizes) download.
		{"mp3", "audio/mpeg", KindAud, true},
		{"m4a", "audio/mp4", KindAud, true},
		{"aac", "audio/aac", KindAud, true},
		{"wav", "audio/wav", KindAud, true},
		{"flac", "audio/flac", KindAud, true},
		{"m4b", "audio/mp4", KindAud, false},
		{"ogg", "audio/ogg", KindAud, false},
		{"oga", "audio/ogg", KindAud, false},
		{"opus", "audio/ogg", KindAud, false},
		{"aiff", "audio/aiff", KindAud, false},
		{"aif", "audio/aiff", KindAud, false},
		{"caf", "audio/x-caf", KindAud, false},
		{"wma", "audio/x-ms-wma", KindAud, false},
		{"amr", "audio/amr", KindAud, false},
		{"mid", "audio/midi", KindAud, false},
		{"midi", "audio/midi", KindAud, false},
		{"kar", "audio/midi", KindAud, false},

		{"zip", "application/zip", KindZip, false},
		{"tar", "application/x-tar", KindZip, false},
		{"gz", "application/gzip", KindZip, false},
		{"7z", "application/x-7z-compressed", KindZip, false},

		{"txt", textPlain, KindTxt, true},
		{"md", textPlain, KindTxt, true},
		{"log", textPlain, KindTxt, true},
		{"json", "application/json", KindTxt, true},
		{"xml", "text/xml; charset=utf-8", KindTxt, true},

		{"html", "text/html; charset=utf-8", KindFile, false},
		{"htm", "text/html; charset=utf-8", KindFile, false},
	} {
		types[r.ext] = Info{MIME: r.mime, Kind: r.kind, Inline: r.inline}
	}
}

// ByExt looks up a lowercase extension without its dot.
func ByExt(ext string) (Info, bool) {
	info, ok := types[ext]
	return info, ok
}

// Lookup classifies a file by name (case-insensitive). Unknown types come
// back as a non-inline application/octet-stream of KindFile.
func Lookup(name string) Info {
	ext := strings.ToLower(strings.TrimPrefix(path.Ext(name), "."))
	if info, ok := types[ext]; ok {
		return info
	}
	return Info{MIME: octet, Kind: KindFile}
}

// All returns every known extension, for the dashboard. The map is a copy.
func All() map[string]Info {
	out := make(map[string]Info, len(types))
	for ext, info := range types {
		out[ext] = info
	}
	return out
}
