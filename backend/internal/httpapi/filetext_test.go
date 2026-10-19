package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/thoff/findo/backend/internal/index"
)

func textURL(volID int64, path, extra string) string {
	return "/files/text?volume=" + strconv.FormatInt(volID, 10) + "&path=" + url.QueryEscape(path) + extra
}

func getText(srv *Server, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, httptest.NewRequest("GET", target, nil))
	return rec
}

func TestFileTextServesExtractedTextAsSandboxedPlainText(t *testing.T) {
	srv, smb, volID := newTestServer(t)
	body := "First paragraph.\n\n<script>alert(1)</script> & <b>second</b>."
	smb.setContent("docs/page.txt", []byte(body), 1)
	optIn(t, srv, volID)
	srv.contentPass(context.Background())

	rec := getText(srv, textURL(volID, "docs/page.txt", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d %s", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Errorf("Content-Type = %q", got)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" || rec.Header().Get("Content-Security-Policy") != "sandbox" {
		t.Errorf("file text must be nosniff and sandboxed: %v", rec.Header())
	}
	if !strings.Contains(rec.Body.String(), "<script>alert(1)</script>") || !strings.Contains(rec.Body.String(), "First paragraph.") {
		t.Errorf("body should be the extracted text, verbatim, as plain text: %q", rec.Body.String())
	}
}

func TestFileTextJSONReturnsChunksInOrder(t *testing.T) {
	srv, smb, volID := newTestServer(t)
	smb.setContent("a.txt", []byte("placeholder"), 1)
	optIn(t, srv, volID)
	if err := srv.idx.SaveContent(volID, "a.txt", 11, 1, []string{"zero", "one", "two"}); err != nil {
		t.Fatal(err)
	}

	rec := getText(srv, textURL(volID, "a.txt", "&format=json"))
	var out struct {
		Name   string   `json:"name"`
		Path   string   `json:"path"`
		Chunks []string `json:"chunks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if out.Name != "a.txt" || out.Path != "a.txt" || strings.Join(out.Chunks, ",") != "zero,one,two" {
		t.Fatalf("got %+v", out)
	}
	if rec.Header().Get("Content-Security-Policy") != "sandbox" {
		t.Error("the JSON form is sandboxed too")
	}
}

func TestFileTextExplainsWhyThereIsNone(t *testing.T) {
	srv, smb, volID := newTestServer(t)
	for _, f := range []string{"never.txt", "big.txt", "bin.txt", "empty.txt", "broken.txt", "ok.txt"} {
		smb.setContent(f, []byte("x"), 1)
	}
	optIn(t, srv, volID)
	idx := srv.idx
	idx.SaveContentSkip(volID, "big.txt", 1, 1, index.ContentSkipped, "too_large")
	idx.SaveContentSkip(volID, "bin.txt", 1, 1, index.ContentSkipped, "unsupported")
	idx.SaveContentSkip(volID, "empty.txt", 1, 1, index.ContentSkipped, "no_text")
	idx.SaveContentSkip(volID, "broken.txt", 1, 1, index.ContentFailed, "access denied")
	idx.SaveContent(volID, "ok.txt", 1, 1, nil) // done, but no chunks

	for path, want := range map[string]string{
		"never.txt":   "hasn't been extracted",
		"big.txt":     "larger than the current size limit",
		"bin.txt":     "isn't readable as text",
		"empty.txt":   "contains no text",
		"broken.txt":  "access denied",
		"ok.txt":      "no extracted text",
		"missing.txt": "hasn't been extracted",
	} {
		rec := getText(srv, textURL(volID, path, ""))
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("%s: %d %s, want 404 mentioning %q", path, rec.Code, rec.Body, want)
		}
	}
}

func TestFileTextValidatesParameters(t *testing.T) {
	srv, _, volID := newTestServer(t)
	for target, want := range map[string]string{
		"/files/text?path=a.txt":                             "missing required query param 'volume'",
		"/files/text?volume=nope&path=a.txt":                 "invalid 'volume'",
		"/files/text?volume=" + strconv.FormatInt(volID, 10): "missing required query param 'path'",
	} {
		rec := getText(srv, target)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("GET %s = %d %s, want 400 %q", target, rec.Code, rec.Body, want)
		}
	}
}

func TestFileTextIgnoresLeadingSlashesLikeOtherPaths(t *testing.T) {
	srv, smb, volID := newTestServer(t)
	smb.setContent("dir/x.txt", []byte("hello there"), 1)
	optIn(t, srv, volID)
	srv.contentPass(context.Background())
	if rec := getText(srv, textURL(volID, "/dir/x.txt", "")); rec.Code != http.StatusOK {
		t.Fatalf("leading slash: %d %s", rec.Code, rec.Body)
	}
}

func TestSearchContentHitCarriesItsChunkNumber(t *testing.T) {
	srv, smb, volID := newTestServer(t)
	smb.setContent("long.txt", []byte("placeholder"), 1)
	optIn(t, srv, volID)
	if err := srv.idx.SaveContent(volID, "long.txt", 11, 1, []string{
		"Nothing relevant in the opening chunk.",
		"The zeppelin schedule is in this middle chunk.",
		"Closing words.",
	}); err != nil {
		t.Fatal(err)
	}

	rec := doJSON(t, srv, "GET", "/search?q=zeppelin", nil)
	var out struct {
		Entries []struct {
			Match string `json:"match"`
			Chunk *int   `json:"chunk"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || len(out.Entries) != 1 {
		t.Fatalf("%s", rec.Body)
	}
	if e := out.Entries[0]; e.Match != "content" || e.Chunk == nil || *e.Chunk != 1 {
		t.Fatalf("entry = %+v, want content match from chunk 1", e)
	}

	// Chunk 0 must survive JSON (not be dropped as an empty value).
	if err := srv.idx.SaveContent(volID, "long.txt", 11, 1, []string{"zeppelin first", "other"}); err != nil {
		t.Fatal(err)
	}
	rec = doJSON(t, srv, "GET", "/search?q=zeppelin", nil)
	if !strings.Contains(rec.Body.String(), `"chunk":0`) {
		t.Fatalf("chunk 0 was dropped from the response: %s", rec.Body)
	}
}
