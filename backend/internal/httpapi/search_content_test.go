package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

type searchResp struct {
	Entries []struct {
		Path    string `json:"path"`
		Match   string `json:"match"`
		Snippet string `json:"snippet"`
	} `json:"entries"`
	ContentMatches int `json:"contentMatches"`
}

func searchFor(t *testing.T, srv *Server, params string) searchResp {
	t.Helper()
	rec := doJSON(t, srv, "GET", "/search?"+params, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /search?%s = %d %s", params, rec.Code, rec.Body)
	}
	var out searchResp
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

func paths(r searchResp) []string {
	var out []string
	for _, e := range r.Entries {
		out = append(out, e.Match+":"+e.Path)
	}
	return out
}

func TestSearchReturnsNameMatchesThenContentMatches(t *testing.T) {
	srv, smb, volID := newTestServer(t)
	smb.setContent("invoice-march.txt", []byte("totals for march"), 100)              // name matches
	smb.setContent("notes/meeting.txt", []byte("Remember to send the invoice."), 200) // content matches
	smb.setContent("recipes/soup.md", []byte("onions and carrots"), 300)              // neither
	optIn(t, srv, volID)
	srv.contentPass(context.Background())

	got := searchFor(t, srv, "q=invoice")
	want := []string{"name:invoice-march.txt", "content:notes/meeting.txt"}
	if fmt.Sprint(paths(got)) != fmt.Sprint(want) {
		t.Fatalf("entries = %v, want %v", paths(got), want)
	}
	if got.ContentMatches != 1 {
		t.Fatalf("contentMatches = %d, want 1", got.ContentMatches)
	}
	if sn := got.Entries[1].Snippet; !strings.Contains(sn, "\x02invoice\x03") {
		t.Fatalf("content entry should carry a highlighted snippet, got %q", sn)
	}
	if got.Entries[0].Snippet != "" {
		t.Fatalf("a name match has no snippet, got %q", got.Entries[0].Snippet)
	}
}

func TestSearchContentMatchesNeedAnOptedInVolume(t *testing.T) {
	srv, smb, volID := newTestServer(t)
	smb.setContent("notes.txt", []byte("the secret word is pineapple"), 1)
	if err := srv.Crawl(volID, "manual"); err != nil {
		t.Fatal(err)
	}
	srv.contentPass(context.Background()) // not opted in: nothing extracted

	if got := searchFor(t, srv, "q=pineapple"); len(got.Entries) != 0 {
		t.Fatalf("a volume that hasn't opted in must not return content matches: %v", paths(got))
	}
	optIn(t, srv, volID)
	srv.contentPass(context.Background())
	if got := searchFor(t, srv, "q=pineapple"); fmt.Sprint(paths(got)) != "[content:notes.txt]" {
		t.Fatalf("after opting in: %v", paths(got))
	}
}

func TestSearchDateOnlyQueriesDoNotSearchContent(t *testing.T) {
	srv, smb, volID := newTestServer(t)
	smb.setContent("a.txt", []byte("last month was busy"), 1)
	optIn(t, srv, volID)
	srv.contentPass(context.Background())

	// "last month" is a date phrase, not words to find in text.
	got := searchFor(t, srv, "q="+url.QueryEscape("last month")+"&modified_after=1970-01-01")
	for _, e := range got.Entries {
		if e.Match == "content" {
			t.Fatalf("a date phrase must not trigger a content search: %v", paths(got))
		}
	}
}

func TestSearchContentFollowsTheDateFilterAndLimit(t *testing.T) {
	srv, smb, volID := newTestServer(t)
	smb.setContent("old.txt", []byte("budget from long ago"), 86400) // 1970-01-02
	smb.setContent("new.txt", []byte("budget from this year"), 1_800_000_000)
	for i := 0; i < 5; i++ {
		smb.setContent(fmt.Sprintf("extra%d.txt", i), []byte("more budget talk"), 1_800_000_000+int64(i))
	}
	optIn(t, srv, volID)
	srv.contentPass(context.Background())

	got := searchFor(t, srv, "q=budget&modified_after=2000-01-01")
	for _, e := range got.Entries {
		if e.Path == "old.txt" {
			t.Fatalf("the date filter must apply to content matches too: %v", paths(got))
		}
	}
	if got := searchFor(t, srv, "q=budget&limit=3"); len(got.Entries) != 3 {
		t.Fatalf("limit=3 returned %d entries", len(got.Entries))
	}
}

func TestSearchWithNoMatchesStillReturnsAnEmptyList(t *testing.T) {
	srv, _, _ := newTestServer(t)
	rec := doJSON(t, srv, "GET", "/search?q=nothing-here", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"entries":null`) {
		t.Fatalf("empty search = %d %s (clients treat null as empty, as before)", rec.Code, rec.Body)
	}
}
