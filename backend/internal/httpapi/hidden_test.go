package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/thoff/findo/backend/internal/index"
)

func TestSearchAndListHideDotfilesUnlessAsked(t *testing.T) {
	srv, _, volID := newTestServer(t)
	err := srv.idx.UpsertBatch([]index.File{
		{VolumeID: volID, Path: "plain-hidden-test.txt", Name: "plain-hidden-test.txt", Ext: "txt"},
		{VolumeID: volID, Path: ".plain-hidden-test", Name: ".plain-hidden-test"},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	h := srv.Routes()
	count := func(url string) int {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", url, nil))
		var body struct{ Entries []index.File }
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s: %v (%s)", url, err, rec.Body.String())
		}
		return len(body.Entries)
	}
	q := "/search?q=plain-hidden-test"
	if got := count(q); got != 1 {
		t.Fatalf("default search = %d entries, want 1", got)
	}
	if got := count(q + "&hidden=1"); got != 2 {
		t.Fatalf("hidden=1 search = %d entries, want 2", got)
	}
	l := fmt.Sprintf("/files?volume=%d&path=", volID)
	if def, all := count(l), count(l+"&hidden=1"); all-def != 1 {
		t.Fatalf("list default=%d hidden=%d, want exactly one extra", def, all)
	}
}
