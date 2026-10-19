// Package filetype is the one place Findo decides what a file is, from its
// extension: the Content-Type to serve, whether a browser may render it
// inline, and the kind that picks its color tile in the dashboard. The table
// is explicit rather than derived from the OS MIME database so behavior
// doesn't depend on the host (macOS and a container disagree).
package filetype

import (
	"path"
	"sort"
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
	// Extractor names how to pull searchable text out of the file ("" means
	// none): see internal/extract, which owns the implementations.
	Extractor string `json:"-"`
}

// Extractor names (see internal/extract).
const (
	ExtText = "text" // plain text, markup, config and source code
	ExtDocx = "docx" // Word (OOXML)
	ExtXlsx = "xlsx" // Excel (OOXML)
	ExtPptx = "pptx" // PowerPoint (OOXML)
	ExtPDF  = "pdf"  // PDF text layer
)

const (
	textPlain = "text/plain; charset=utf-8"
	octet     = "application/octet-stream"
)

var types = map[string]Info{}

// add registers extensions under one definition. A repeated extension is a
// mistake in this table, so it panics at startup (and in every test) rather
// than silently letting the later entry win.
func add(mime, kind string, inline bool, exts ...string) {
	for _, ext := range exts {
		if _, dup := types[ext]; dup {
			panic("filetype: duplicate extension " + ext)
		}
		types[ext] = Info{MIME: mime, Kind: kind, Inline: inline}
	}
}

func setExtractor(name string, exts ...string) {
	for _, ext := range exts {
		info, ok := types[ext]
		if !ok {
			panic("filetype: extractor for unknown extension " + ext)
		}
		info.Extractor = name
		types[ext] = info
	}
}

func init() {
	add("application/pdf", KindPDF, true, "pdf")

	add("application/msword", KindDoc, false, "doc")
	add("application/vnd.openxmlformats-officedocument.wordprocessingml.document", KindDoc, false, "docx")
	add("application/vnd.ms-word.document.macroEnabled.12", KindDoc, false, "docm")
	add("application/vnd.openxmlformats-officedocument.wordprocessingml.template", KindDoc, false, "dotx")
	add("application/vnd.apple.pages", KindDoc, false, "pages")
	add("application/rtf", KindDoc, false, "rtf")

	add("application/vnd.ms-excel", KindXLS, false, "xls")
	add("application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", KindXLS, false, "xlsx")
	add("application/vnd.ms-excel.sheet.macroEnabled.12", KindXLS, false, "xlsm")
	add("application/vnd.openxmlformats-officedocument.spreadsheetml.template", KindXLS, false, "xltx")
	add("application/vnd.apple.numbers", KindXLS, false, "numbers")
	add(textPlain, KindXLS, true, "csv", "tsv")

	add("application/vnd.ms-powerpoint", KindPPT, false, "ppt")
	add("application/vnd.openxmlformats-officedocument.presentationml.presentation", KindPPT, false, "pptx")
	add("application/vnd.ms-powerpoint.presentation.macroEnabled.12", KindPPT, false, "pptm")
	add("application/vnd.openxmlformats-officedocument.presentationml.slideshow", KindPPT, false, "ppsx")
	add("application/vnd.openxmlformats-officedocument.presentationml.template", KindPPT, false, "potx")
	add("application/vnd.apple.keynote", KindPPT, false, "key")

	// Images. Inline only what Safari and Chromium both render; HEIC, TIFF,
	// JPEG XL and the like download. SVG is an image but never inline (it can
	// carry script).
	add("image/jpeg", KindImg, true, "jpg", "jpeg", "jfif")
	add("image/png", KindImg, true, "png")
	add("image/apng", KindImg, true, "apng")
	add("image/gif", KindImg, true, "gif")
	add("image/webp", KindImg, true, "webp")
	add("image/avif", KindImg, true, "avif")
	add("image/bmp", KindImg, true, "bmp")
	add("image/x-icon", KindImg, true, "ico")
	add("image/svg+xml", KindImg, false, "svg")
	add("image/heic", KindImg, false, "heic")
	add("image/heif", KindImg, false, "heif")
	add("image/tiff", KindImg, false, "tif", "tiff")
	add("image/jxl", KindImg, false, "jxl")
	add("image/jp2", KindImg, false, "jp2", "j2k")
	add("image/vnd.adobe.photoshop", KindImg, false, "psd")
	add("application/postscript", KindImg, false, "ai", "eps")
	add("image/icns", KindImg, false, "icns")
	add("image/x-tga", KindImg, false, "tga")
	add("image/x-xcf", KindImg, false, "xcf")
	// Camera raw.
	add("image/x-canon-cr2", KindImg, false, "cr2")
	add("image/x-canon-cr3", KindImg, false, "cr3")
	add("image/x-nikon-nef", KindImg, false, "nef")
	add("image/x-sony-arw", KindImg, false, "arw")
	add("image/x-adobe-dng", KindImg, false, "dng")
	add("image/x-olympus-orf", KindImg, false, "orf")
	add("image/x-panasonic-rw2", KindImg, false, "rw2")
	add("image/x-fuji-raf", KindImg, false, "raf")
	add("image/x-pentax-pef", KindImg, false, "pef")

	// Video. Inline: containers/codecs Safari and Chromium both play.
	// ".ts" is deliberately absent: it's both MPEG transport stream and
	// TypeScript, and serving a video as text would be far worse than a
	// download.
	add("video/mp4", KindVid, true, "mp4", "m4v")
	add("video/quicktime", KindVid, true, "mov")
	add("video/webm", KindVid, true, "webm")
	add("video/x-msvideo", KindVid, false, "avi")
	add("video/x-matroska", KindVid, false, "mkv")
	add("video/x-ms-wmv", KindVid, false, "wmv")
	add("video/x-ms-asf", KindVid, false, "asf")
	add("video/x-flv", KindVid, false, "flv")
	add("video/x-f4v", KindVid, false, "f4v")
	add("video/mpeg", KindVid, false, "mpg", "mpeg", "mpe", "m2v", "vob")
	add("video/mp2t", KindVid, false, "m2ts", "mts")
	add("video/3gpp", KindVid, false, "3gp")
	add("video/3gpp2", KindVid, false, "3g2")
	add("video/ogg", KindVid, false, "ogv")
	add("video/divx", KindVid, false, "divx")
	add("video/x-dv", KindVid, false, "dv")
	add("application/mxf", KindVid, false, "mxf")
	add("application/vnd.rn-realmedia", KindVid, false, "rm")
	add("application/vnd.rn-realmedia-vbr", KindVid, false, "rmvb")

	// Audio: inline only for what Safari and Chromium both play; the rest
	// (Apple's AIFF/CAF, MIDI, which no browser synthesizes, ...) download.
	add("audio/mpeg", KindAud, true, "mp3")
	add("audio/mp4", KindAud, true, "m4a")
	add("audio/aac", KindAud, true, "aac")
	add("audio/wav", KindAud, true, "wav")
	add("audio/flac", KindAud, true, "flac")
	add("audio/mp4", KindAud, false, "m4b", "m4p", "m4r") // audiobook, DRM-protected AAC, ringtone
	add("audio/aac", KindAud, false, "adts")
	add("audio/ogg", KindAud, false, "ogg", "oga", "opus")
	add("audio/aiff", KindAud, false, "aiff", "aif", "aifc")
	add("audio/x-caf", KindAud, false, "caf")
	add("audio/x-ms-wma", KindAud, false, "wma")
	add("audio/amr", KindAud, false, "amr")
	add("audio/basic", KindAud, false, "au", "snd")
	add("audio/ac3", KindAud, false, "ac3")
	add("audio/eac3", KindAud, false, "ec3")
	add("audio/midi", KindAud, false, "mid", "midi", "kar")

	add("application/zip", KindZip, false, "zip")
	add("application/x-tar", KindZip, false, "tar")
	add("application/gzip", KindZip, false, "gz")
	add("application/x-7z-compressed", KindZip, false, "7z")

	// Plain text and source code share the text tile (the tile palette is
	// full; the extension printed on it tells them apart). All are served as
	// text/plain so a browser shows them instead of executing or guessing.
	add(textPlain, KindTxt, true, "txt", "md", "mdx", "rst", "adoc", "org", "log")
	add("application/json", KindTxt, true, "json")
	add("text/xml; charset=utf-8", KindTxt, true, "xml")
	codeExts := strings.Fields(`
		c h cc cpp cxx c++ hpp hh hxx m mm cs java kt kts scala groovy go rs zig
		py pyw rb php pl pm lua r jl swift dart hs ml mli erl hrl ex exs clj cljs
		lisp el vim asm s
		js mjs cjs jsx tsx vue svelte css scss sass less
		sh bash zsh fish ps1 psm1 bat cmd
		sql graphql gql proto tf hcl nix gradle mk cmake
		yaml yml toml ini cfg conf properties env jsonc json5
		tex bib diff patch gitignore gitattributes editorconfig
	`)
	add(textPlain, KindTxt, true, codeExts...)

	setExtractor(ExtPDF, "pdf")
	setExtractor(ExtDocx, "docx", "docm", "dotx")
	setExtractor(ExtXlsx, "xlsx", "xlsm", "xltx")
	setExtractor(ExtPptx, "pptx", "pptm", "ppsx", "potx")
	setExtractor(ExtText, append([]string{"txt", "md", "mdx", "rst", "adoc", "org", "log", "json", "xml", "csv", "tsv"}, codeExts...)...)

	// Markup that can run script on this origin: known, never inline.
	add("text/html; charset=utf-8", KindFile, false, "html", "htm")
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

// ExtsWithExtractor returns, sorted, the extensions whose extractor is one of
// names.
func ExtsWithExtractor(names ...string) []string {
	var out []string
	for ext, info := range types {
		for _, n := range names {
			if info.Extractor == n {
				out = append(out, ext)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

// All returns every known extension, for the dashboard. The map is a copy.
func All() map[string]Info {
	out := make(map[string]Info, len(types))
	for ext, info := range types {
		out[ext] = info
	}
	return out
}
