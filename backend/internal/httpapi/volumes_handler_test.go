package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/thoff/findo/backend/internal/crypto"
	"github.com/thoff/findo/backend/internal/index"
	"github.com/thoff/findo/backend/internal/smbclient"
)

// newTestServerNoVolumes returns a Server with no volumes configured yet,
// its newSMB overridden to hand out smb regardless of the connection
// details a handler passes it — for exercising the CRUD/test endpoints'
// real StartVolume path end to end.
func newTestServerNoVolumes(t *testing.T, smb *fakeSMB) *Server {
	t.Helper()
	idx, err := index.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("index.Open: %v", err)
	}
	t.Cleanup(func() { idx.Close() })

	srv := NewServer(idx, make([]byte, crypto.KeySize))
	srv.newSMB = func(host, share, user, pass string) smbConnector { return smb }
	return srv
}

func doJSON(t *testing.T, srv *Server, method, path string, body interface{}) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode body: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)
	return rec
}

func TestCreateVolumeStartsItAndOmitsPassword(t *testing.T) {
	srv := newTestServerNoVolumes(t, newFakeSMB())

	rec := doJSON(t, srv, "POST", "/volumes", map[string]string{
		"name": "Home NAS", "host": "nas.local", "share": "media", "username": "findo", "password": "s3cret",
	})
	if rec.Code != 201 {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	if bytes.Contains(rec.Body.Bytes(), []byte("s3cret")) {
		t.Fatalf("response must never echo the password: %s", rec.Body.String())
	}

	var created volumeStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if created.ID == 0 || created.Name != "Home NAS" {
		t.Fatalf("unexpected created volume: %+v", created)
	}

	waitFor(t, func() bool {
		return srv.volumeRuntime(created.ID) != nil
	})
}

func TestCreateVolumeRejectsMissingFields(t *testing.T) {
	srv := newTestServerNoVolumes(t, newFakeSMB())
	rec := doJSON(t, srv, "POST", "/volumes", map[string]string{"name": "Incomplete"})
	if rec.Code != 400 {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestListVolumesReportsLiveState(t *testing.T) {
	srv, smb, volID := newTestServer(t)
	smb.set(smbclient.Entry{Path: "a.txt", Name: "a.txt"})
	if err := srv.Crawl(volID, "manual"); err != nil {
		t.Fatalf("Crawl: %v", err)
	}

	rec := doJSON(t, srv, "GET", "/volumes", nil)
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Volumes []volumeStatus `json:"volumes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.Volumes) != 1 {
		t.Fatalf("expected 1 volume, got %d", len(out.Volumes))
	}
	v := out.Volumes[0]
	if !v.Connected {
		t.Fatalf("expected the volume to report connected")
	}
	if v.FileCount != 1 {
		t.Fatalf("expected fileCount 1, got %d", v.FileCount)
	}
	if v.LastCrawlStartedAt == "" {
		t.Fatalf("expected a recorded crawl start time")
	}
	if v.LastCrawlTrigger != "manual" {
		t.Fatalf("expected trigger %q, got %q", "manual", v.LastCrawlTrigger)
	}
	if v.LastCrawlFilesSeen != 1 {
		t.Fatalf("expected lastCrawlFilesSeen 1, got %d", v.LastCrawlFilesSeen)
	}
}

func TestCrawlRunsEndpointReturnsHistory(t *testing.T) {
	srv, smb, volID := newTestServer(t)
	smb.set(smbclient.Entry{Path: "a.txt", Name: "a.txt", Size: 42})
	if err := srv.Crawl(volID, "manual"); err != nil {
		t.Fatalf("Crawl: %v", err)
	}
	if err := srv.Crawl(volID, "periodic"); err != nil {
		t.Fatalf("Crawl: %v", err)
	}

	rec := doJSON(t, srv, "GET", "/volumes/"+strconv.FormatInt(volID, 10)+"/crawl-runs", nil)
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Runs []index.CrawlRun `json:"runs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.Runs) != 2 {
		t.Fatalf("expected 2 runs, got %d", len(out.Runs))
	}
	if out.Runs[0].Trigger != "periodic" {
		t.Fatalf("expected the newest run (periodic) first, got %+v", out.Runs[0])
	}
	if out.Runs[1].Trigger != "manual" {
		t.Fatalf("expected the oldest run (manual) last, got %+v", out.Runs[1])
	}
}

func TestDeleteVolumeStopsRuntimeAndData(t *testing.T) {
	srv, smb, volID := newTestServer(t)
	smb.set(smbclient.Entry{Path: "a.txt", Name: "a.txt"})
	if err := srv.Crawl(volID, "manual"); err != nil {
		t.Fatalf("Crawl: %v", err)
	}

	rec := doJSON(t, srv, "DELETE", "/volumes/"+strconv.FormatInt(volID, 10), nil)
	if rec.Code != 204 {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}

	if srv.volumeRuntime(volID) != nil {
		t.Fatalf("expected the runtime to be torn down")
	}
	if _, ok, _ := srv.idx.GetVolume(volID); ok {
		t.Fatalf("expected the volume to be gone")
	}
}

func TestTestVolumeInputReportsFailure(t *testing.T) {
	smb := newFakeSMB()
	smb.connectErr = errFakeListing
	srv := newTestServerNoVolumes(t, smb)

	rec := doJSON(t, srv, "POST", "/volumes/test", map[string]string{
		"host": "h", "share": "s", "username": "u", "password": "p",
	})
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.OK || out.Error == "" {
		t.Fatalf("expected a reported failure, got %+v", out)
	}
}

func TestReindexReturnsConflictWhileCrawling(t *testing.T) {
	srv, _, volID := newTestServer(t)
	srv.volumeRuntime(volID).crawling.Store(true)

	rec := doJSON(t, srv, "POST", "/volumes/"+strconv.FormatInt(volID, 10)+"/reindex", nil)
	if rec.Code != 409 {
		t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestVolumeInsightsReturnsExtensionsAndLargestFiles(t *testing.T) {
	srv, smb, volID := newTestServer(t)
	smb.set(smbclient.Entry{Path: "a.pdf", Name: "a.pdf", Size: 100})
	smb.set(smbclient.Entry{Path: "b.jpg", Name: "b.jpg", Size: 900})
	if err := srv.Crawl(volID, "manual"); err != nil {
		t.Fatalf("Crawl: %v", err)
	}

	rec := doJSON(t, srv, "GET", "/volumes/"+strconv.FormatInt(volID, 10)+"/insights", nil)
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Extensions   []index.ExtStat `json:"extensions"`
		LargestFiles []index.File    `json:"largestFiles"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.Extensions) != 2 || out.Extensions[0].Ext != "jpg" {
		t.Fatalf("expected jpg first by size, got %+v", out.Extensions)
	}
	if len(out.LargestFiles) != 2 || out.LargestFiles[0].Name != "b.jpg" {
		t.Fatalf("expected b.jpg first by size, got %+v", out.LargestFiles)
	}
}
