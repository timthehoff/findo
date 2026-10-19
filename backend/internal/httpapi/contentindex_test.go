package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/thoff/findo/backend/internal/extract"
	"github.com/thoff/findo/backend/internal/index"
)

// optIn turns on content indexing for the test volume and crawls the fake NAS
// so the files table matches what the fake serves.
func optIn(t *testing.T, srv *Server, volID int64) {
	t.Helper()
	if _, err := srv.idx.SetVolumeContentIndex(volID, true); err != nil {
		t.Fatal(err)
	}
	if err := srv.Crawl(volID, "manual"); err != nil {
		t.Fatalf("Crawl: %v", err)
	}
}

func chunkTexts(t *testing.T, srv *Server, volID int64, path string) []string {
	t.Helper()
	info, _, err := srv.idx.ContentFor(volID, path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Chunks
}

type contentRow struct{ status, reason string }

func contentStateOf(t *testing.T, srv *Server, volID int64, path string) (contentRow, bool) {
	t.Helper()
	info, ok, err := srv.idx.ContentFor(volID, path)
	if err != nil {
		t.Fatal(err)
	}
	return contentRow{info.Status, info.Reason}, ok
}

// attempted counts the volume's files with any recorded outcome.
func attempted(t *testing.T, srv *Server) (done, other int) {
	t.Helper()
	prog, err := srv.idx.ContentProgress(extract.Supported(), 1<<40)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range prog {
		done += p.Done
		other += p.Skipped + p.Failed
	}
	return done, other
}

func TestContentPassIndexesTextAndRecordsWhyOthersWerent(t *testing.T) {
	srv, smb, volID := newTestServer(t)
	smb.setContent("notes/budget.txt", []byte("Quarterly budget forecast.\n\nSecond paragraph."), 100)
	smb.setContent("src/main.go", []byte("package main\n\nfunc main() {}\n"), 100)
	smb.setContent("data/blob.txt", []byte("looks like text\x00but is binary"), 100)
	smb.setContent("empty.md", []byte("   \n\n  "), 100)
	smb.setContent("photo.png", []byte("not read: no extractor"), 100)
	smb.setContent(".hidden/secret.txt", []byte("hidden"), 100)
	smb.setContent("big.txt", []byte(strings.Repeat("x", 3<<20)), 100)
	smb.setContent("unreadable.txt", []byte("fine"), 100)
	smb.openErr = map[string]error{"unreadable.txt": errors.New("access denied")}
	optIn(t, srv, volID)

	// A 1 MiB limit: big.txt (3 MiB) is skipped without being read.
	if err := srv.idx.SetContentSettings(index.ContentSettings{MaxFileBytes: 1 << 20, MaxTextBytes: 1 << 20, Workers: 1}); err != nil {
		t.Fatal(err)
	}

	if !srv.contentPass(context.Background()) {
		t.Fatal("a pass with pending files should report progress")
	}

	if got := chunkTexts(t, srv, volID, "notes/budget.txt"); len(got) != 1 || !strings.Contains(got[0], "Quarterly budget forecast.") || !strings.Contains(got[0], "Second paragraph.") {
		t.Fatalf("budget.txt chunks = %q", got)
	}
	if got := chunkTexts(t, srv, volID, "src/main.go"); len(got) != 1 || !strings.Contains(got[0], "func main") {
		t.Fatalf("main.go chunks = %q", got)
	}
	for path, want := range map[string]contentRow{
		"notes/budget.txt": {"done", ""},
		"data/blob.txt":    {"skipped", "unsupported"},
		"empty.md":         {"skipped", "no_text"},
		"big.txt":          {"skipped", "too_large"},
		"unreadable.txt":   {"failed", "open unreadable.txt: access denied"},
	} {
		got, ok := contentStateOf(t, srv, volID, path)
		if !ok || got.status != want.status || (want.reason != "" && !strings.Contains(got.reason, "access denied") && got.reason != want.reason) {
			t.Errorf("%s: state = %+v (recorded=%v), want %+v", path, got, ok, want)
		}
	}
	for _, path := range []string{"photo.png", ".hidden/secret.txt"} {
		if _, ok := contentStateOf(t, srv, volID, path); ok {
			t.Errorf("%s should never have been attempted", path)
		}
	}

	// Everything is accounted for, so the next pass finds nothing to do.
	if srv.contentPass(context.Background()) {
		t.Fatal("a second pass over an unchanged volume should be idle")
	}
}

func TestContentPassIdleWhenPausedNotOptedInOrCrawling(t *testing.T) {
	srv, smb, volID := newTestServer(t)
	smb.setContent("a.txt", []byte("hello"), 1)
	if err := srv.Crawl(volID, "manual"); err != nil {
		t.Fatal(err)
	}

	if srv.contentPass(context.Background()) {
		t.Fatal("a volume that hasn't opted in must not be indexed")
	}
	if _, err := srv.idx.SetVolumeContentIndex(volID, true); err != nil {
		t.Fatal(err)
	}

	cs := index.DefaultContentSettings()
	cs.Paused = true
	srv.idx.SetContentSettings(cs)
	if srv.contentPass(context.Background()) {
		t.Fatal("a paused indexer must not work")
	}
	cs.Paused = false
	srv.idx.SetContentSettings(cs)

	rt := srv.volumeRuntime(volID)
	rt.crawling.Store(true)
	if srv.contentPass(context.Background()) {
		t.Fatal("a volume that is mid-crawl must be left alone")
	}
	rt.crawling.Store(false)

	if !srv.contentPass(context.Background()) {
		t.Fatal("with everything enabled, the file should be indexed")
	}
	if got := chunkTexts(t, srv, volID, "a.txt"); len(got) != 1 {
		t.Fatalf("a.txt chunks = %q", got)
	}
}

func TestContentPassReindexesAChangedFile(t *testing.T) {
	srv, smb, volID := newTestServer(t)
	smb.setContent("doc.txt", []byte("first version"), 100)
	optIn(t, srv, volID)
	srv.contentPass(context.Background())
	if got := chunkTexts(t, srv, volID, "doc.txt"); len(got) != 1 || got[0] != "first version" {
		t.Fatalf("v1 chunks = %q", got)
	}

	smb.setContent("doc.txt", []byte("second, longer version"), 200)
	if err := srv.Crawl(volID, "manual"); err != nil {
		t.Fatal(err)
	}
	if !srv.contentPass(context.Background()) {
		t.Fatal("a changed file should be picked up")
	}
	if got := chunkTexts(t, srv, volID, "doc.txt"); len(got) != 1 || got[0] != "second, longer version" {
		t.Fatalf("v2 chunks = %q (the old text should be replaced)", got)
	}
}

func TestContentPassCutsOffAStalledRead(t *testing.T) {
	srv, smb, volID := newTestServer(t)
	srv.contentTimeout = 50 * time.Millisecond
	smb.setContent("stuck.txt", []byte("never read"), 1)
	smb.setContent("fine.txt", []byte("readable"), 1)
	smb.blockOpen = map[string]bool{"stuck.txt": true}
	optIn(t, srv, volID)

	done := make(chan bool, 1)
	go func() { done <- srv.contentPass(context.Background()) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the pass hung on a stalled read")
	}

	if st, ok := contentStateOf(t, srv, volID, "stuck.txt"); !ok || st.status != "failed" || !strings.Contains(st.reason, "timed out") {
		t.Fatalf("stuck.txt = %+v (recorded=%v), want failed/timed out", st, ok)
	}
	if st, ok := contentStateOf(t, srv, volID, "fine.txt"); !ok || st.status != "done" {
		t.Fatalf("a stall must not affect other files: fine.txt = %+v", st)
	}
}

func TestContentPassWithSeveralWorkers(t *testing.T) {
	srv, smb, volID := newTestServer(t)
	for i := 0; i < 30; i++ {
		smb.setContent(fmt.Sprintf("f%02d.txt", i), []byte(fmt.Sprintf("content of file %d", i)), 1)
	}
	optIn(t, srv, volID)
	cs := index.DefaultContentSettings()
	cs.Workers = 4
	srv.idx.SetContentSettings(cs)

	srv.contentPass(context.Background())

	if done, _ := attempted(t, srv); done != 30 {
		t.Fatalf("indexed %d of 30 files with 4 workers", done)
	}
}

func TestContentPassStopsWhenCancelled(t *testing.T) {
	srv, smb, volID := newTestServer(t)
	for i := 0; i < 5; i++ {
		smb.setContent(fmt.Sprintf("f%d.txt", i), []byte("x"), 1)
	}
	optIn(t, srv, volID)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if srv.contentPass(ctx) {
		t.Fatal("a cancelled pass should report no progress")
	}
	if done, other := attempted(t, srv); done+other != 0 {
		t.Fatalf("a cancelled pass recorded %d files (it must not record failures caused by shutdown)", done+other)
	}
}

func TestRunContentIndexerWakesOnKick(t *testing.T) {
	srv, smb, volID := newTestServer(t)
	srv.contentIdle = time.Hour // only a kick can wake it
	optIn(t, srv, volID)

	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { srv.RunContentIndexer(ctx); close(stopped) }()

	smb.setContent("late.txt", []byte("arrived after the indexer went idle"), 5)
	if err := srv.Crawl(volID, "manual"); err != nil { // Crawl kicks the indexer when it finishes
		t.Fatal(err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(chunkTexts(t, srv, volID, "late.txt")) == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(chunkTexts(t, srv, volID, "late.txt")) != 1 {
		t.Fatal("the indexer never picked up the new file after a crawl")
	}

	cancel()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("RunContentIndexer did not stop on cancel")
	}
}

func TestContentStatusAndRetryEndpoints(t *testing.T) {
	srv, smb, volID := newTestServer(t)
	smb.setContent("ok.txt", []byte("fine"), 1)
	smb.setContent("bad.txt", []byte("fine"), 1)
	smb.openErr = map[string]error{"bad.txt": errors.New("access denied")}
	optIn(t, srv, volID)
	srv.contentPass(context.Background())

	rec := doJSON(t, srv, "GET", "/content/status", nil)
	var status struct {
		Paused  bool                    `json:"paused"`
		Volumes []index.ContentProgress `json:"volumes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("GET /content/status = %d %s", rec.Code, rec.Body)
	}
	if len(status.Volumes) != 1 || status.Volumes[0].Done != 1 || status.Volumes[0].Failed != 1 || status.Volumes[0].Eligible != 2 {
		t.Fatalf("status = %+v", status)
	}
	if len(status.Volumes[0].Reasons) != 1 || status.Volumes[0].Reasons[0].Status != "failed" {
		t.Fatalf("reasons = %+v", status.Volumes[0].Reasons)
	}

	// The failure was transient: fix it, retry, and the file is indexed.
	smb.openErr = nil
	rec = doJSON(t, srv, "POST", "/volumes/"+strconv.FormatInt(volID, 10)+"/content/retry", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"retried":1`) {
		t.Fatalf("retry = %d %s", rec.Code, rec.Body)
	}
	select {
	case <-srv.contentKick:
	default:
		t.Fatal("retry should wake the indexer")
	}
	srv.contentPass(context.Background())
	if st, _ := contentStateOf(t, srv, volID, "bad.txt"); st.status != "done" {
		t.Fatalf("after retry bad.txt = %+v", st)
	}

	if rec := doJSON(t, srv, "POST", "/volumes/nope/content/retry", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad volume id = %d, want 400", rec.Code)
	}
}

func TestSettingsAndOptInKickTheIndexer(t *testing.T) {
	srv, _, volID := newTestServer(t)
	drain := func() {
		select {
		case <-srv.contentKick:
		default:
		}
	}

	drain()
	doJSON(t, srv, "PUT", "/settings", map[string]interface{}{"content": map[string]interface{}{"paused": false}})
	select {
	case <-srv.contentKick:
	default:
		t.Fatal("saving settings should wake the indexer")
	}

	drain()
	doJSON(t, srv, "PUT", "/volumes/"+strconv.FormatInt(volID, 10), map[string]interface{}{
		"name": "test volume", "host": "host", "share": "share", "username": "user", "contentIndex": true,
	})
	select {
	case <-srv.contentKick:
	default:
		t.Fatal("opting a volume in should wake the indexer")
	}
}

func TestContentPassIndexesOfficeFilesAndReportsEncryptedOnes(t *testing.T) {
	srv, smb, volID := newTestServer(t)
	for name, file := range map[string]string{
		"memo.docx": "memo.docx", "budget.xlsx": "budget.xlsx", "roadmap.pptx": "roadmap.pptx",
	} {
		data, err := os.ReadFile("../../testdata/seed/office/" + file)
		if err != nil {
			t.Fatal(err)
		}
		smb.setContent("office/"+name, data, 1)
	}
	// A password-protected document is an OLE file wearing a .docx name.
	smb.setContent("office/locked.docx", append([]byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}, make([]byte, 200)...), 1)
	smb.setContent("office/corrupt.xlsx", []byte("PK but not really a zip"), 1)
	optIn(t, srv, volID)
	srv.contentPass(context.Background())

	for path, want := range map[string]string{
		"office/memo.docx":    "done",
		"office/budget.xlsx":  "done",
		"office/roadmap.pptx": "done",
		"office/locked.docx":  "skipped",
		"office/corrupt.xlsx": "failed",
	} {
		st, ok := contentStateOf(t, srv, volID, path)
		if !ok || st.status != want {
			t.Errorf("%s = %+v (recorded=%v), want %s", path, st, ok, want)
		}
	}
	if st, _ := contentStateOf(t, srv, volID, "office/locked.docx"); st.reason != "encrypted" {
		t.Errorf("locked.docx reason = %q, want encrypted", st.reason)
	}

	// Searchable, with the viewer's text behind it.
	got := searchFor(t, srv, "q=hydrogen")
	if fmt.Sprint(paths(got)) != "[content:office/roadmap.pptx]" {
		t.Fatalf("search for a word in speaker notes: %v", paths(got))
	}
	if got := searchFor(t, srv, "q=zeppelin"); len(got.Entries) != 2 {
		t.Fatalf("zeppelin appears in the docx and the xlsx, and nowhere else: %v", paths(got))
	}
	rec := getText(srv, textURL(volID, "office/memo.docx", ""))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Quarterly Planning Memo") {
		t.Fatalf("/files/text for a docx = %d %s", rec.Code, rec.Body)
	}
	rec = getText(srv, textURL(volID, "office/locked.docx", ""))
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "password-protected") {
		t.Fatalf("/files/text for a locked file = %d %s", rec.Code, rec.Body)
	}
}
