package httpapi

import (
	"net/http"
	"path"
	"strings"

	"github.com/thoff/findo/backend/internal/index"
)

// handleFileText serves a file's extracted text, the same text content search
// runs on, so formats a browser can't render (Office files, once their
// extractors exist) are still readable. It is text, not a copy of the file:
// whitespace is normalized and chunks are separated by blank lines.
//
// By default it returns text/plain; ?format=json returns the chunks one by
// one for the viewer page, which scrolls to the chunk a search hit came from.
// Like /files/content it is served nosniff and sandboxed, so file text can
// never run as markup on this origin.
func (s *Server) handleFileText(w http.ResponseWriter, r *http.Request) {
	volumeID, err := parseVolumeIDParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	p := strings.Trim(r.URL.Query().Get("path"), "/")
	if p == "" {
		writeError(w, http.StatusBadRequest, "missing required query param 'path'")
		return
	}

	info, ok, err := s.idx.ContentFor(volumeID, p)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if msg := noTextReason(info, ok); msg != "" {
		writeError(w, http.StatusNotFound, msg)
		return
	}

	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "sandbox")

	if r.URL.Query().Get("format") == "json" {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"volumeId": volumeID, "path": p, "name": path.Base(p), "chunks": info.Chunks,
		})
		return
	}
	h.Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(strings.Join(info.Chunks, "\n\n") + "\n"))
}

// noTextReason explains why a file has no extracted text to show, or returns
// "" when it does.
func noTextReason(info index.ContentInfo, attempted bool) string {
	if !attempted {
		return "this file's text hasn't been extracted: content search may be off for its volume, the file may be a type that can't be read yet, or it may still be queued"
	}
	switch info.Status {
	case index.ContentDone:
		if len(info.Chunks) > 0 {
			return ""
		}
		return "this file has no extracted text"
	case index.ContentFailed:
		return "text extraction failed for this file: " + info.Reason
	}
	switch info.Reason {
	case "too_large":
		return "this file is larger than the current size limit for content indexing"
	case "unsupported":
		return "this file isn't readable as text"
	case "no_text":
		return "this file contains no text"
	}
	return "this file was skipped: " + info.Reason
}
