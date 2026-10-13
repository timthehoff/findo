package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestIsNewer(t *testing.T) {
	cases := []struct {
		latest, current string
		want            bool
	}{
		{"v1.2.4", "1.2.3", true},
		{"v1.10.0", "1.9.9", true}, // numeric, not lexicographic
		{"v2.0.0", "1.9.9", true},
		{"v1.2.3", "1.2.3", false},
		{"v1.2.3", "1.2.4", false},
		{"v1.2.4", "1.2.3-rc1", true},
		{"v1.2.4", "dev", false},
		{"garbage", "1.2.3", false},
	}
	for _, c := range cases {
		if got := isNewer(c.latest, c.current); got != c.want {
			t.Errorf("isNewer(%q, %q) = %v, want %v", c.latest, c.current, got, c.want)
		}
	}
}

type versionResp struct {
	Current         string `json:"current"`
	Latest          string `json:"latest"`
	UpdateAvailable bool   `json:"updateAvailable"`
	URL             string `json:"url"`
}

func getVersion(t *testing.T, srv *Server) versionResp {
	t.Helper()
	rec := doJSON(t, srv, "GET", "/version", nil)
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var v versionResp
	if err := json.NewDecoder(rec.Body).Decode(&v); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return v
}

func TestVersionReportsAvailableUpdateAndCaches(t *testing.T) {
	srv := newTestServerNoVolumes(t, newFakeSMB())
	srv.version = "1.0.0"
	calls := 0
	srv.updates.fetch = func(context.Context) (release, error) {
		calls++
		return release{Tag: "v1.1.0", URL: "https://github.com/timthehoff/findo/releases/tag/v1.1.0"}, nil
	}

	v := getVersion(t, srv)
	if !v.UpdateAvailable || v.Latest != "v1.1.0" || v.URL == "" || v.Current != "1.0.0" {
		t.Fatalf("unexpected response: %+v", v)
	}
	getVersion(t, srv)
	if calls != 1 {
		t.Fatalf("expected the lookup to be cached, fetch called %d times", calls)
	}
}

func TestVersionUpToDate(t *testing.T) {
	srv := newTestServerNoVolumes(t, newFakeSMB())
	srv.version = "1.1.0"
	srv.updates.fetch = func(context.Context) (release, error) { return release{Tag: "v1.1.0"}, nil }

	if v := getVersion(t, srv); v.UpdateAvailable {
		t.Fatalf("same version must not report an update: %+v", v)
	}
}

func TestVersionLookupFailureIsNotAnError(t *testing.T) {
	srv := newTestServerNoVolumes(t, newFakeSMB())
	srv.version = "1.0.0"
	now := time.Now()
	srv.updates.now = func() time.Time { return now }
	calls := 0
	srv.updates.fetch = func(context.Context) (release, error) {
		calls++
		return release{}, errors.New("offline")
	}

	if v := getVersion(t, srv); v.UpdateAvailable || v.Latest != "" || v.Current != "1.0.0" {
		t.Fatalf("unexpected response: %+v", v)
	}
	getVersion(t, srv)
	if calls != 1 {
		t.Fatalf("failure should be cached briefly, fetch called %d times", calls)
	}

	now = now.Add(releaseRetryTTL + time.Minute)
	getVersion(t, srv)
	if calls != 2 {
		t.Fatalf("expected a retry after the retry TTL, fetch called %d times", calls)
	}
}

func TestVersionDevBuildSkipsLookup(t *testing.T) {
	srv := newTestServerNoVolumes(t, newFakeSMB())
	srv.updates.fetch = func(context.Context) (release, error) {
		t.Fatal("dev builds must not query GitHub")
		return release{}, nil
	}

	if v := getVersion(t, srv); v.Current != "dev" || v.UpdateAvailable {
		t.Fatalf("unexpected response: %+v", v)
	}
}
