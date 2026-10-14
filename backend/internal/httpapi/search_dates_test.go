package httpapi

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/thoff/findo/backend/internal/index"
)

func TestSearchDateFilters(t *testing.T) {
	srv, _, volID := newTestServer(t)
	now := time.Now()
	day := func(n int) int64 { return now.AddDate(0, 0, -n).Unix() }
	err := srv.idx.UpsertBatch([]index.File{
		{VolumeID: volID, Path: "datetest-new.txt", Name: "datetest-new.txt", Ext: "txt", ModTime: day(2)},
		{VolumeID: volID, Path: "datetest-mid.txt", Name: "datetest-mid.txt", Ext: "txt", ModTime: day(10)},
		{VolumeID: volID, Path: "datetest-old.txt", Name: "datetest-old.txt", Ext: "txt", ModTime: day(400)},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	h := srv.Routes()
	search := func(url string) (int, []string, map[string]interface{}) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", url, nil))
		var body struct {
			Entries  []index.File
			Modified map[string]interface{}
		}
		if rec.Code == 200 {
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("%s: %v", url, err)
			}
		}
		var names []string
		for _, e := range body.Entries {
			names = append(names, e.Name)
		}
		return rec.Code, names, body.Modified
	}

	// Phrase in q: newest first, phrase reported back, name text still applies.
	code, names, mod := search("/search?q=datetest+last+month&tz=UTC")
	if code != 200 || len(names) != 2 || names[0] != "datetest-new.txt" || names[1] != "datetest-mid.txt" {
		t.Fatalf("last month: code=%d names=%v", code, names)
	}
	if mod["phrase"] != "last month" {
		t.Fatalf("modified = %v", mod)
	}

	// A phrase alone (no name text) lists everything in range.
	if _, names, _ = search("/search?q=past+week&tz=UTC"); len(names) != 1 || names[0] != "datetest-new.txt" {
		t.Fatalf("past week: %v", names)
	}

	// Explicit params work without any phrase, and need no q.
	before := now.AddDate(0, 0, -100).Format("2006-01-02")
	if code, names, _ = search("/search?q=datetest&modified_before=" + before); code != 200 || len(names) != 1 || names[0] != "datetest-old.txt" {
		t.Fatalf("modified_before: code=%d names=%v", code, names)
	}
	if code, names, _ = search("/search?modified_after=" + before + "&tz=UTC"); code != 200 || len(names) < 2 {
		t.Fatalf("modified_after without q: code=%d names=%v", code, names)
	}

	// Unfiltered search is unchanged; bad inputs are 400s.
	if _, names, mod = search("/search?q=datetest"); len(names) != 3 || mod != nil {
		t.Fatalf("plain search: %v %v", names, mod)
	}
	for _, bad := range []string{"/search", "/search?q=x&tz=Not/AZone", "/search?q=x&modified_after=soon"} {
		if code, _, _ = search(bad); code != 400 {
			t.Errorf("%s = %d, want 400", bad, code)
		}
	}
}
