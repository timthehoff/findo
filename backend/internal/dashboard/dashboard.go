// Package dashboard serves a small static multi-page site for poking the
// running index: index.html (volume config + crawl/watch monitoring) and
// search.html (search and directory browsing), sharing css/ and js/ files
// between them. No build step — plain HTML/CSS/JS (ES modules) calling the
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
	return http.FileServer(http.FS(sub))
}
