package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLandingPageIsSearchAndOldURLRedirects(t *testing.T) {
	srv, _, _ := newTestServer(t)
	h := srv.Routes()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `id="q"`) {
		t.Fatalf("GET / = %d, want the search page", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/volumes.html", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "addVolumeBtn") {
		t.Fatalf("GET /volumes.html = %d, want the volumes page", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/search.html", nil))
	if rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != "/" {
		t.Fatalf("GET /search.html = %d -> %q, want 301 -> /", rec.Code, rec.Header().Get("Location"))
	}
}

func TestTextViewerPageIsServed(t *testing.T) {
	srv, _, _ := newTestServer(t)
	for _, target := range []string{"/text.html", "/css/text.css", "/js/text.js"} {
		rec := httptest.NewRecorder()
		srv.Routes().ServeHTTP(rec, httptest.NewRequest("GET", target, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d", target, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, httptest.NewRequest("GET", "/text.html", nil))
	if !strings.Contains(rec.Body.String(), `id="chunks"`) {
		t.Fatal("/text.html should be the viewer page")
	}
}
