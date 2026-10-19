// Package dashboard serves a small static multi-page site for poking the
// running index: index.html, the landing page (search and directory
// browsing), and volumes.html (volume config + crawl/watch monitoring),
// sharing css/ and js/ files between them. No build step — plain HTML/CSS/JS (ES modules) calling the
// JSON API via fetch.
package dashboard

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed static
var staticFiles embed.FS

func Handler() http.Handler {
	sub, err := fs.Sub(staticFiles, "static")
	if err != nil {
		panic(err) // embedded at build time; can't fail at runtime
	}
	return withSecurityHeaders(http.FileServer(http.FS(sub)))
}

// withSecurityHeaders applies a strict Content-Security-Policy and friends to
// the dashboard pages. Everything the pages load is same-origin (modules,
// stylesheets, the JSON API); the one concession is inline style attributes
// (progress-bar widths). If markup from a file ever reached a page, this
// stops it running or phoning home, and it keeps other sites from framing the
// dashboard to trick clicks.
func withSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}
