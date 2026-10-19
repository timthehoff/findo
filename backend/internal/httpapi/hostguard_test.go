package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHostAllowed(t *testing.T) {
	extra := map[string]bool{"findo.example.com": true, "mac.tail1234.ts.net": true}
	for host, want := range map[string]bool{
		// This machine, and addresses: nothing a rebinding page can produce.
		"localhost": true, "localhost:8080": true, "LOCALHOST:8080": true,
		"127.0.0.1": true, "127.0.0.1:8080": true, "[::1]:8080": true, "[::1]": true,
		"192.168.1.20:8080": true, "10.0.0.5": true, "[fe80::1]:8080": true,
		// Names that only resolve locally.
		"macstudio": true, "macstudio:8080": true,
		"macstudio.local": true, "Tims-Mac-Studio.local:8080": true,
		"nas.lan": true, "printer.home.arpa": true, "box.internal": true, "x.localdomain": true,
		"foo.localhost": true, "macstudio.local.": true,
		// Explicitly listed names.
		"findo.example.com": true, "FINDO.example.com:443": true, "mac.tail1234.ts.net": true,
		// Public names: where DNS rebinding comes from.
		"evil.example.com": false, "evil.example.com:8080": false, "example.com": false,
		"192.168.1.20.evil.example.com": false, "localhost.evil.example.com": false,
		"macstudio.local.evil.com": false, "notlocal": true, // single label: still local-only
		"": false, "   ": false,
	} {
		if got := hostAllowed(host, extra); got != want {
			t.Errorf("hostAllowed(%q) = %v, want %v", host, got, want)
		}
	}
}

func guarded(t *testing.T, extra ...string) http.Handler {
	t.Helper()
	return HostGuard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}), extra)
}

func do(h http.Handler, method, host, origin string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/volumes", strings.NewReader("{}"))
	req.Host = host
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestHostGuardRefusesRebindingHosts(t *testing.T) {
	h := guarded(t)
	if rec := do(h, "GET", "localhost:8080", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("localhost = %d", rec.Code)
	}
	rec := do(h, "GET", "evil.example.com", "")
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "FINDO_ALLOWED_HOSTS") {
		t.Fatalf("a public Host = %d %s, want 403 explaining FINDO_ALLOWED_HOSTS", rec.Code, rec.Body)
	}
	// The same name works once the operator lists it.
	if rec := do(guarded(t, "evil.example.com"), "GET", "evil.example.com", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("a listed Host = %d", rec.Code)
	}
}

func TestHostGuardRefusesCrossSiteStateChanges(t *testing.T) {
	h := guarded(t)
	for _, method := range []string{"POST", "PUT", "DELETE", "PATCH"} {
		for name, origin := range map[string]string{
			"another site":         "https://evil.example.com",
			"another port":         "http://localhost:9999",
			"another scheme+host":  "http://192.168.1.50:8080",
			"opaque origin (null)": "null",
			"malformed":            "::::",
		} {
			if rec := do(h, method, "localhost:8080", origin); rec.Code != http.StatusForbidden {
				t.Errorf("%s with Origin from %s = %d, want 403", method, name, rec.Code)
			}
		}
		// The dashboard's own requests, and clients that send no Origin, pass.
		if rec := do(h, method, "localhost:8080", "http://localhost:8080"); rec.Code != http.StatusNoContent {
			t.Errorf("%s same-origin = %d", method, rec.Code)
		}
		if rec := do(h, method, "macstudio.local:8080", "http://MacStudio.local:8080"); rec.Code != http.StatusNoContent {
			t.Errorf("%s same-origin, different case = %d", method, rec.Code)
		}
		if rec := do(h, method, "localhost:8080", ""); rec.Code != http.StatusNoContent {
			t.Errorf("%s with no Origin (curl, scripts) = %d", method, rec.Code)
		}
	}
	// Reads aren't state changes (and the browser blocks a cross-origin read anyway).
	for _, method := range []string{"GET", "HEAD", "OPTIONS"} {
		if rec := do(h, method, "localhost:8080", "https://evil.example.com"); rec.Code != http.StatusNoContent {
			t.Errorf("%s with a foreign Origin = %d, want it let through", method, rec.Code)
		}
	}
}
