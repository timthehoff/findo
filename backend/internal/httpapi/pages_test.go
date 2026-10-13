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
