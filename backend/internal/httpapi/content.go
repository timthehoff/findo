package httpapi

import (
	"mime"
	"net/http"
	"path"
	"strings"
)

// inlineTypes are the file types browsers (Safari and Chromium) can render
// themselves, keyed by lowercase extension. Everything else is served as a
// download. The map is explicit rather than derived from the OS MIME table
// so behavior doesn't depend on what the container image happens to ship.
// HTML and SVG are deliberately absent: rendering NAS-supplied markup on
// the dashboard's origin would let it script against the API.
var inlineTypes = map[string]string{
	".pdf":  "application/pdf",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".png":  "image/png",
	".gif":  "image/gif",
	".webp": "image/webp",
	".avif": "image/avif",
	".txt":  "text/plain; charset=utf-8",
	".log":  "text/plain; charset=utf-8",
	".md":   "text/plain; charset=utf-8",
	".csv":  "text/plain; charset=utf-8",
	".json": "application/json",
	".xml":  "text/xml; charset=utf-8",
	".mp3":  "audio/mpeg",
	".m4a":  "audio/mp4",
	".wav":  "audio/wav",
	".mp4":  "video/mp4",
	".m4v":  "video/mp4",
	".mov":  "video/quicktime",
	".webm": "video/webm",
}

// setContentHeaders sets Content-Type and Content-Disposition for serving
// name: inline for browser-renderable types, attachment otherwise or when
// forceDownload is set. The sandbox CSP and nosniff keep a file from
// running as active content even if one slips through as inline.
func setContentHeaders(h http.Header, name string, forceDownload bool) {
	ext := strings.ToLower(path.Ext(name))

	ctype, inline := inlineTypes[ext]
	if !inline {
		ctype = mime.TypeByExtension(ext)
		if ctype == "" {
			ctype = "application/octet-stream"
		}
	}

	disposition := "attachment"
	if inline && !forceDownload {
		disposition = "inline"
	}

	h.Set("Content-Type", ctype)
	h.Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": name}))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "sandbox")
}
