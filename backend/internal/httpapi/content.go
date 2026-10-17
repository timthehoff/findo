package httpapi

import (
	"mime"
	"net/http"

	"github.com/thoff/findo/backend/internal/filetype"
)

// setContentHeaders sets Content-Type and Content-Disposition for serving
// name: inline for browser-renderable types (see internal/filetype),
// attachment otherwise or when forceDownload is set. The sandbox CSP and
// nosniff keep a file from running as active content even if one slips
// through as inline.
func setContentHeaders(h http.Header, name string, forceDownload bool) {
	info := filetype.Lookup(name)

	disposition := "attachment"
	if info.Inline && !forceDownload {
		disposition = "inline"
	}

	h.Set("Content-Type", info.MIME)
	h.Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": name}))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "sandbox")
}

// handleFileTypes serves the extension registry so the dashboard colors its
// type tiles from the same table the server uses to serve files.
func (s *Server) handleFileTypes(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "public, max-age=3600")
	writeJSON(w, http.StatusOK, map[string]interface{}{"types": filetype.All()})
}
