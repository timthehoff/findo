package index

import (
	"fmt"
	"path/filepath"
	"testing"
)

// testVolume is used as the volume id throughout these tests. The files/
// crawl_runs tables don't enforce a foreign key to volumes, so tests that
// only exercise file indexing don't need a real volumes row.
const testVolume = int64(1)

func openTest(t *testing.T) *Index {
	t.Helper()
	idx, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { idx.Close() })
	return idx
}

func mustRun(t *testing.T, idx *Index) int64 {
	t.Helper()
	id, err := idx.StartCrawlRun(testVolume, "manual")
	if err != nil {
		t.Fatalf("StartCrawlRun: %v", err)
	}
	return id
}

func TestUpsertBatchInsertsAndUpdates(t *testing.T) {
	idx := openTest(t)

	run1 := mustRun(t, idx)
	f := File{VolumeID: testVolume, Path: "docs/budget.xlsx", Name: "budget.xlsx", Dir: "docs", Ext: "xlsx", Size: 100, ModTime: 1000}
	if err := idx.UpsertBatch([]File{f}, run1); err != nil {
		t.Fatalf("UpsertBatch: %v", err)
	}

	got, ok, err := idx.Stat(testVolume, "docs/budget.xlsx")
	if err != nil || !ok {
		t.Fatalf("Stat after insert: ok=%v err=%v", ok, err)
	}
	if got.Size != 100 || got.ModTime != 1000 {
		t.Fatalf("unexpected file after insert: %+v", got)
	}

	// Re-upsert the same path with changed metadata under a new run — should
	// update in place, not duplicate.
	run2 := mustRun(t, idx)
	f.Size = 200
	f.ModTime = 2000
	if err := idx.UpsertBatch([]File{f}, run2); err != nil {
		t.Fatalf("UpsertBatch update: %v", err)
	}

	got, ok, err = idx.Stat(testVolume, "docs/budget.xlsx")
	if err != nil || !ok {
		t.Fatalf("Stat after update: ok=%v err=%v", ok, err)
	}
	if got.Size != 200 || got.ModTime != 2000 {
		t.Fatalf("update not applied: %+v", got)
	}

	all, err := idx.Search(testVolume, "budget", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("expected exactly one row for the path, got %d", len(all))
	}
}

func TestUpsertBatchScopesPathsPerVolume(t *testing.T) {
	idx := openTest(t)

	run1, err := idx.StartCrawlRun(1, "manual")
	if err != nil {
		t.Fatalf("StartCrawlRun: %v", err)
	}
	if err := idx.UpsertBatch([]File{{VolumeID: 1, Path: "a.txt", Name: "a.txt", Size: 10}}, run1); err != nil {
		t.Fatalf("UpsertBatch volume 1: %v", err)
	}

	run2, err := idx.StartCrawlRun(2, "manual")
	if err != nil {
		t.Fatalf("StartCrawlRun: %v", err)
	}
	if err := idx.UpsertBatch([]File{{VolumeID: 2, Path: "a.txt", Name: "a.txt", Size: 99}}, run2); err != nil {
		t.Fatalf("UpsertBatch volume 2: %v", err)
	}

	f1, ok, err := idx.Stat(1, "a.txt")
	if err != nil || !ok {
		t.Fatalf("Stat volume 1: ok=%v err=%v", ok, err)
	}
	if f1.Size != 10 {
		t.Fatalf("volume 1's a.txt should be untouched by volume 2's upsert, got size %d", f1.Size)
	}

	f2, ok, err := idx.Stat(2, "a.txt")
	if err != nil || !ok {
		t.Fatalf("Stat volume 2: ok=%v err=%v", ok, err)
	}
	if f2.Size != 99 {
		t.Fatalf("expected volume 2's a.txt size 99, got %d", f2.Size)
	}
}

func TestSweepRemovesUnseenRows(t *testing.T) {
	idx := openTest(t)

	run1 := mustRun(t, idx)
	files := []File{
		{VolumeID: testVolume, Path: "a.txt", Name: "a.txt", Dir: "", Ext: "txt"},
		{VolumeID: testVolume, Path: "b.txt", Name: "b.txt", Dir: "", Ext: "txt"},
	}
	if err := idx.UpsertBatch(files, run1); err != nil {
		t.Fatalf("UpsertBatch: %v", err)
	}

	// Next crawl only sees a.txt — b.txt was deleted on the NAS.
	run2 := mustRun(t, idx)
	if err := idx.UpsertBatch([]File{files[0]}, run2); err != nil {
		t.Fatalf("UpsertBatch run2: %v", err)
	}
	if _, err := idx.Sweep(testVolume, run2); err != nil {
		t.Fatalf("Sweep: %v", err)
	}

	if _, ok, err := idx.Stat(testVolume, "a.txt"); err != nil || !ok {
		t.Fatalf("a.txt should survive the sweep: ok=%v err=%v", ok, err)
	}
	if _, ok, err := idx.Stat(testVolume, "b.txt"); err != nil || ok {
		t.Fatalf("b.txt should have been swept: ok=%v err=%v", ok, err)
	}
}

func TestReconcileEndToEnd(t *testing.T) {
	idx := openTest(t)

	run1 := mustRun(t, idx)
	initial := []File{
		{VolumeID: testVolume, Path: "keep.txt", Name: "keep.txt", Dir: "", Ext: "txt", Size: 1},
		{VolumeID: testVolume, Path: "gone.txt", Name: "gone.txt", Dir: "", Ext: "txt", Size: 1},
	}
	if err := idx.Reconcile(testVolume, initial, run1); err != nil {
		t.Fatalf("Reconcile initial: %v", err)
	}

	run2 := mustRun(t, idx)
	updated := []File{
		{VolumeID: testVolume, Path: "keep.txt", Name: "keep.txt", Dir: "", Ext: "txt", Size: 999},
	}
	if err := idx.Reconcile(testVolume, updated, run2); err != nil {
		t.Fatalf("Reconcile updated: %v", err)
	}

	got, ok, err := idx.Stat(testVolume, "keep.txt")
	if err != nil || !ok {
		t.Fatalf("keep.txt should still exist: ok=%v err=%v", ok, err)
	}
	if got.Size != 999 {
		t.Fatalf("keep.txt should have the updated size, got %d", got.Size)
	}
	if _, ok, err := idx.Stat(testVolume, "gone.txt"); err != nil || ok {
		t.Fatalf("gone.txt should have been reconciled away: ok=%v err=%v", ok, err)
	}
}

func TestUpsertAndDeleteSingleFile(t *testing.T) {
	idx := openTest(t)

	run := mustRun(t, idx)
	f := File{VolumeID: testVolume, Path: "note.md", Name: "note.md", Dir: "", Ext: "md"}
	if err := idx.Upsert(f, run); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if _, ok, err := idx.Stat(testVolume, "note.md"); err != nil || !ok {
		t.Fatalf("note.md should exist after Upsert: ok=%v err=%v", ok, err)
	}

	if err := idx.Delete(testVolume, "note.md"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok, err := idx.Stat(testVolume, "note.md"); err != nil || ok {
		t.Fatalf("note.md should be gone after Delete: ok=%v err=%v", ok, err)
	}
}

func TestStreamUpsertBatches(t *testing.T) {
	idx := openTest(t)
	run := mustRun(t, idx)

	entries := make(chan File, 10)
	for i := 0; i < 5; i++ {
		p := fmt.Sprintf("f%d.txt", i)
		entries <- File{VolumeID: testVolume, Path: p, Name: p}
	}
	close(entries)

	// batchSize=2 with 5 entries forces multiple UpsertBatch calls plus a
	// final partial flush — exercises the batching boundary, not just the
	// single-batch case.
	written, err := idx.StreamUpsert(entries, run, 2)
	if err != nil {
		t.Fatalf("StreamUpsert: %v", err)
	}
	if written != 5 {
		t.Fatalf("expected 5 written, got %d", written)
	}

	all, err := idx.Search(testVolume, "f", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(all) != 5 {
		t.Fatalf("expected 5 rows indexed, got %d", len(all))
	}
}

func TestStreamUpsertKeepsDrainingAfterWriteError(t *testing.T) {
	idx := openTest(t)
	run := mustRun(t, idx)

	entries := make(chan File, 10)
	for i := 0; i < 4; i++ {
		p := fmt.Sprintf("g%d.txt", i)
		entries <- File{VolumeID: testVolume, Path: p, Name: p}
	}
	close(entries)
	idx.Close() // force every subsequent UpsertBatch call to fail

	written, err := idx.StreamUpsert(entries, run, 2)
	if err == nil {
		t.Fatalf("expected an error once the database is closed")
	}
	if written != 0 {
		t.Fatalf("expected 0 written once every batch fails, got %d", written)
	}
}

func TestSearchAcrossAllVolumes(t *testing.T) {
	idx := openTest(t)

	run1, err := idx.StartCrawlRun(1, "manual")
	if err != nil {
		t.Fatalf("StartCrawlRun: %v", err)
	}
	if err := idx.UpsertBatch([]File{{VolumeID: 1, Path: "report.pdf", Name: "report.pdf"}}, run1); err != nil {
		t.Fatalf("UpsertBatch: %v", err)
	}

	run2, err := idx.StartCrawlRun(2, "manual")
	if err != nil {
		t.Fatalf("StartCrawlRun: %v", err)
	}
	if err := idx.UpsertBatch([]File{{VolumeID: 2, Path: "report-final.pdf", Name: "report-final.pdf"}}, run2); err != nil {
		t.Fatalf("UpsertBatch: %v", err)
	}

	all, err := idx.Search(0, "report", 10)
	if err != nil {
		t.Fatalf("Search across all volumes: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 results across both volumes, got %d: %+v", len(all), all)
	}

	scoped, err := idx.Search(1, "report", 10)
	if err != nil {
		t.Fatalf("Search scoped to volume 1: %v", err)
	}
	if len(scoped) != 1 || scoped[0].VolumeID != 1 {
		t.Fatalf("expected 1 result scoped to volume 1, got %+v", scoped)
	}
}

func TestFinishCrawlRunRecordsResult(t *testing.T) {
	idx := openTest(t)

	runID, err := idx.StartCrawlRun(testVolume, "resync")
	if err != nil {
		t.Fatalf("StartCrawlRun: %v", err)
	}
	res := CrawlRunResult{FilesSeen: 3, FilesRemoved: 1, BytesIndexed: 4096, DurationMS: 1500}
	if err := idx.FinishCrawlRun(runID, res); err != nil {
		t.Fatalf("FinishCrawlRun: %v", err)
	}

	got, ok, err := idx.LatestCrawlRun(testVolume)
	if err != nil || !ok {
		t.Fatalf("LatestCrawlRun: ok=%v err=%v", ok, err)
	}
	if got.Trigger != "resync" || got.FilesSeen != 3 || got.FilesRemoved != 1 ||
		got.BytesIndexed != 4096 || got.DurationMS != 1500 || got.FinishedAt == "" {
		t.Fatalf("unexpected recorded run: %+v", got)
	}
	if got.Error != "" {
		t.Fatalf("expected no error recorded, got %q", got.Error)
	}
}

func TestFinishCrawlRunRecordsError(t *testing.T) {
	idx := openTest(t)

	runID, err := idx.StartCrawlRun(testVolume, "manual")
	if err != nil {
		t.Fatalf("StartCrawlRun: %v", err)
	}
	if err := idx.FinishCrawlRun(runID, CrawlRunResult{Error: fmt.Errorf("boom")}); err != nil {
		t.Fatalf("FinishCrawlRun: %v", err)
	}

	got, ok, err := idx.LatestCrawlRun(testVolume)
	if err != nil || !ok {
		t.Fatalf("LatestCrawlRun: ok=%v err=%v", ok, err)
	}
	if got.Error != "boom" {
		t.Fatalf("expected recorded error \"boom\", got %q", got.Error)
	}
}

func TestCrawlRunsReturnsNewestFirst(t *testing.T) {
	idx := openTest(t)

	var lastID int64
	for i, trigger := range []string{"startup", "manual", "periodic"} {
		id, err := idx.StartCrawlRun(testVolume, trigger)
		if err != nil {
			t.Fatalf("StartCrawlRun: %v", err)
		}
		if err := idx.FinishCrawlRun(id, CrawlRunResult{FilesSeen: i}); err != nil {
			t.Fatalf("FinishCrawlRun: %v", err)
		}
		lastID = id
	}

	runs, err := idx.CrawlRuns(testVolume, 10)
	if err != nil {
		t.Fatalf("CrawlRuns: %v", err)
	}
	if len(runs) != 3 {
		t.Fatalf("expected 3 runs, got %d", len(runs))
	}
	if runs[0].ID != lastID || runs[0].Trigger != "periodic" {
		t.Fatalf("expected the most recent run (periodic) first, got %+v", runs[0])
	}
	if runs[2].Trigger != "startup" {
		t.Fatalf("expected the oldest run last, got %+v", runs[2])
	}
}

func TestExtensionBreakdownRanksByTotalSize(t *testing.T) {
	idx := openTest(t)
	run := mustRun(t, idx)

	files := []File{
		{VolumeID: testVolume, Path: "a.pdf", Name: "a.pdf", Ext: "pdf", Size: 100},
		{VolumeID: testVolume, Path: "b.pdf", Name: "b.pdf", Ext: "pdf", Size: 50},
		{VolumeID: testVolume, Path: "c.jpg", Name: "c.jpg", Ext: "jpg", Size: 300},
		{VolumeID: testVolume, Path: "noext", Name: "noext", Ext: "", Size: 10},
		{VolumeID: testVolume, Path: "dir", Name: "dir", IsDir: true, Size: 0},
	}
	if err := idx.UpsertBatch(files, run); err != nil {
		t.Fatalf("UpsertBatch: %v", err)
	}

	stats, err := idx.ExtensionBreakdown(testVolume, 10)
	if err != nil {
		t.Fatalf("ExtensionBreakdown: %v", err)
	}
	if len(stats) != 3 {
		t.Fatalf("expected 3 extension groups (dirs excluded), got %d: %+v", len(stats), stats)
	}
	if stats[0].Ext != "jpg" || stats[0].TotalSize != 300 || stats[0].Count != 1 {
		t.Fatalf("expected jpg first with total 300, got %+v", stats[0])
	}
	if stats[1].Ext != "pdf" || stats[1].TotalSize != 150 || stats[1].Count != 2 {
		t.Fatalf("expected pdf second with total 150 across 2 files, got %+v", stats[1])
	}
	if stats[2].Ext != "(none)" || stats[2].TotalSize != 10 {
		t.Fatalf("expected extensionless files grouped under (none), got %+v", stats[2])
	}
}

func TestLargestFilesOrdersBySizeDescending(t *testing.T) {
	idx := openTest(t)
	run := mustRun(t, idx)

	files := []File{
		{VolumeID: testVolume, Path: "small.txt", Name: "small.txt", Size: 10},
		{VolumeID: testVolume, Path: "big.mov", Name: "big.mov", Size: 9000},
		{VolumeID: testVolume, Path: "medium.pdf", Name: "medium.pdf", Size: 500},
		{VolumeID: testVolume, Path: "dir", Name: "dir", IsDir: true, Size: 999999},
	}
	if err := idx.UpsertBatch(files, run); err != nil {
		t.Fatalf("UpsertBatch: %v", err)
	}

	largest, err := idx.LargestFiles(testVolume, 2)
	if err != nil {
		t.Fatalf("LargestFiles: %v", err)
	}
	if len(largest) != 2 {
		t.Fatalf("expected 2 results (limit applied), got %d", len(largest))
	}
	if largest[0].Name != "big.mov" || largest[1].Name != "medium.pdf" {
		t.Fatalf("expected big.mov then medium.pdf, got %+v", largest)
	}
}
