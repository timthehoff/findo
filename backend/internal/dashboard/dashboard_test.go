package dashboard

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEveryPageLinksTheServedFavicon(t *testing.T) {
	srv := httptest.NewServer(Handler())
	defer srv.Close()

	for _, page := range []string{"index.html", "volumes.html", "text.html"} {
		body := get(t, srv.URL+"/"+page)
		if !strings.Contains(body.text, `<link rel="icon" href="/favicon.svg" type="image/svg+xml">`) {
			t.Errorf("%s has no favicon link", page)
		}
	}

	icon := get(t, srv.URL+"/favicon.svg")
	if icon.status != http.StatusOK || icon.contentType != "image/svg+xml" {
		t.Errorf("favicon: status %d, type %q", icon.status, icon.contentType)
	}
	if !strings.Contains(icon.text, "🐾") {
		t.Error("favicon should be the brand emoji")
	}
}

type resp struct {
	status      int
	contentType string
	text        string
}

func get(t *testing.T, url string) resp {
	t.Helper()
	r, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	var b strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := r.Body.Read(buf)
		b.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return resp{r.StatusCode, strings.Split(r.Header.Get("Content-Type"), ";")[0], b.String()}
}
