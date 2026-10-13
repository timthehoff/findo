package httpapi

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

// The filename-suffixed content URL must reach handleContent, not fall
// through to the static dashboard.
func TestContentRouteAcceptsFilenameSuffix(t *testing.T) {
	srv, _, volID := newTestServer(t)
	h := srv.Routes()

	for _, url := range []string{
		"/files/content?volume=%d",
		"/files/content/report.pdf?volume=%d",
		"/files/content/my%%20file%%20(1).pdf?volume=%d",
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", fmt.Sprintf(url, volID), nil))
		if rec.Code != 400 || !strings.Contains(rec.Body.String(), "missing required query param 'path'") {
			t.Errorf("GET %s = %d %q, want the handler's 400 for a missing path", url, rec.Code, rec.Body.String())
		}
	}
}
