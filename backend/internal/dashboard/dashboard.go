// Package dashboard serves a minimal static HTML/JS page for poking the
// running index: health/stats plus ad-hoc list/search queries. No build
// step — plain HTML calling the JSON API via fetch.
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
