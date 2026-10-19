package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/thoff/findo/backend/internal/index"
)

func TestOversizedJSONBodiesAreRejected(t *testing.T) {
	srv, _, volID := newTestServer(t)
	big := `{"name":"` + strings.Repeat("a", maxJSONBody+10) + `"}`
	for method, target := range map[string]string{
		"POST": "/volumes",
		"PUT":  "/volumes/" + strconv.FormatInt(volID, 10),
	} {
		req, rr := rawJSON(method, target, big)
		srv.Routes().ServeHTTP(rr, req)
		if rr.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("%s %s with a %d-byte body = %d %s, want 413", method, target, len(big), rr.Code, rr.Body)
		}
	}
	req, rr := rawJSON("PUT", "/settings", `{"content":{"paused":`+strings.Repeat(" ", maxJSONBody)+`true}}`)
	srv.Routes().ServeHTTP(rr, req)
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("PUT /settings oversized = %d, want 413", rr.Code)
	}
	req, rr = rawJSON("POST", "/volumes/test", `{"host":"`+strings.Repeat("h", maxJSONBody)+`"}`)
	srv.Routes().ServeHTTP(rr, req)
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("POST /volumes/test oversized = %d, want 413", rr.Code)
	}

	// Ordinary bodies are untouched, and bad JSON is still a 400.
	if rec := doJSON(t, srv, "PUT", "/settings", map[string]interface{}{"content": map[string]interface{}{"paused": true}}); rec.Code != http.StatusOK {
		t.Errorf("a normal body = %d", rec.Code)
	}
	req, rr = rawJSON("PUT", "/settings", `{not json`)
	srv.Routes().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("malformed JSON = %d, want 400", rr.Code)
	}
}

func TestPathsWithDotDotAreRefused(t *testing.T) {
	srv, smb, volID := newTestServer(t)
	smb.setContent("docs/a.txt", []byte("hello"), 1)
	vol := strconv.FormatInt(volID, 10)
	for _, p := range []string{
		"..", "../etc/passwd", "docs/../../etc/passwd", `..\..\etc\passwd`, `docs\..\..\x`,
		"/../etc/passwd", "docs/../..", "a/b/../../../c", "ok\x00/../x",
	} {
		for _, route := range []string{"/files/content", "/files", "/files/text"} {
			rec := getText(srv, route+"?volume="+vol+"&path="+url.QueryEscape(p))
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid 'path'") {
				t.Errorf("GET %s path=%q = %d %s, want 400", route, p, rec.Code, rec.Body)
			}
		}
	}
	// Ordinary paths, including dots that are not a parent reference, are not
	// mistaken for traversal: they get past validation (a missing file is a 404).
	for _, p := range []string{"docs/..hidden", "docs/a..b", "./docs", "docs/a.txt"} {
		rec := getText(srv, "/files/content?volume="+vol+"&path="+url.QueryEscape(p))
		if rec.Code == http.StatusBadRequest {
			t.Errorf("path %q was refused as traversal: %s", p, rec.Body)
		}
	}
	if rec := getText(srv, "/files?volume="+vol+"&path=docs"); rec.Code != http.StatusOK {
		t.Errorf("listing an ordinary directory = %d", rec.Code)
	}
}

func TestChangingConnectionDetailsRequiresThePasswordAgain(t *testing.T) {
	srv := newTestServerNoVolumes(t, newFakeSMB())
	create := doJSON(t, srv, "POST", "/volumes", map[string]interface{}{
		"name": "nas", "host": "nas.lan", "share": "docs", "username": "ann", "password": "secret",
	})
	var vol index.Volume
	decode(t, create.Body.Bytes(), &vol)
	target := "/volumes/" + strconv.FormatInt(vol.ID, 10)

	base := func() map[string]interface{} {
		return map[string]interface{}{"name": "nas", "host": "nas.lan", "share": "docs", "username": "ann"}
	}
	for field, value := range map[string]string{"host": "attacker.example.com:445", "share": "other", "username": "bob"} {
		body := base()
		body[field] = value
		rec := doJSON(t, srv, "PUT", target, body)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "requires entering the password again") {
			t.Errorf("changing %s without a password = %d %s, want 400", field, rec.Code, rec.Body)
		}
	}
	got, _, _ := srv.idx.GetVolume(vol.ID)
	if got.Host != "nas.lan" || got.Share != "docs" || got.Username != "ann" {
		t.Fatalf("a refused change must not be applied: %+v", got)
	}

	// Supplying the password makes it a deliberate, authenticated change.
	body := base()
	body["host"] = "newnas.lan"
	body["password"] = "secret"
	if rec := doJSON(t, srv, "PUT", target, body); rec.Code != http.StatusOK {
		t.Fatalf("changing the host with the password = %d %s", rec.Code, rec.Body)
	}
	// Changes that don't affect where or as whom it connects need no password.
	body = map[string]interface{}{"name": "renamed", "host": "newnas.lan", "share": "docs", "username": "ann", "enabled": true, "contentIndex": true}
	if rec := doJSON(t, srv, "PUT", target, body); rec.Code != http.StatusOK {
		t.Fatalf("a rename without the password = %d %s", rec.Code, rec.Body)
	}
}

func TestDashboardPagesCarrySecurityHeaders(t *testing.T) {
	srv, _, _ := newTestServer(t)
	for _, target := range []string{"/", "/volumes.html", "/text.html", "/js/search.js"} {
		rec := getText(srv, target)
		h := rec.Header()
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d", target, rec.Code)
		}
		csp := h.Get("Content-Security-Policy")
		for _, want := range []string{"default-src 'self'", "frame-ancestors 'none'", "base-uri 'none'"} {
			if !strings.Contains(csp, want) {
				t.Errorf("%s: CSP %q is missing %q", target, csp, want)
			}
		}
		if strings.Contains(csp, "script-src") && strings.Contains(csp, "'unsafe-inline'") && strings.Contains(csp, "script-src 'unsafe") {
			t.Errorf("%s: scripts must not be allowed inline: %q", target, csp)
		}
		if h.Get("X-Frame-Options") != "DENY" || h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Referrer-Policy") != "no-referrer" {
			t.Errorf("%s: missing a security header: %v", target, h)
		}
	}
	// API responses are nosniff, and file content keeps its own stricter sandbox CSP.
	if rec := getText(srv, "/stats"); rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("JSON responses should be nosniff: %v", rec.Header())
	}
	h := http.Header{}
	setContentHeaders(h, "a.pdf", false)
	if h.Get("Content-Security-Policy") != "sandbox" {
		t.Errorf("file content must keep its sandbox CSP, got %q", h.Get("Content-Security-Policy"))
	}
}

func rawJSON(method, target, body string) (*http.Request, *httptest.ResponseRecorder) {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return req, httptest.NewRecorder()
}

func decode(t *testing.T, b []byte, v interface{}) {
	t.Helper()
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("decode %s: %v", b, err)
	}
}
