package index

import (
	"path/filepath"
	"testing"
)

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
	id, err := idx.StartCrawlRun()
	if err != nil {
		t.Fatalf("StartCrawlRun: %v", err)
	}
	return id
}

func TestUpsertBatchInsertsAndUpdates(t *testing.T) {
	idx := openTest(t)

	run1 := mustRun(t, idx)
	f := File{Path: "docs/budget.xlsx", Name: "budget.xlsx", Dir: "docs", Ext: "xlsx", Size: 100, ModTime: 1000}
	if err := idx.UpsertBatch([]File{f}, run1); err != nil {
		t.Fatalf("UpsertBatch: %v", err)
	}

	got, ok, err := idx.Stat("docs/budget.xlsx")
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

	got, ok, err = idx.Stat("docs/budget.xlsx")
	if err != nil || !ok {
		t.Fatalf("Stat after update: ok=%v err=%v", ok, err)
	}
	if got.Size != 200 || got.ModTime != 2000 {
		t.Fatalf("update not applied: %+v", got)
	}

	all, err := idx.Search("budget", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("expected exactly one row for the path, got %d", len(all))
	}
}

func TestSweepRemovesUnseenRows(t *testing.T) {
	idx := openTest(t)

	run1 := mustRun(t, idx)
	files := []File{
		{Path: "a.txt", Name: "a.txt", Dir: "", Ext: "txt"},
		{Path: "b.txt", Name: "b.txt", Dir: "", Ext: "txt"},
	}
	if err := idx.UpsertBatch(files, run1); err != nil {
		t.Fatalf("UpsertBatch: %v", err)
	}

	// Next crawl only sees a.txt — b.txt was deleted on the NAS.
	run2 := mustRun(t, idx)
	if err := idx.UpsertBatch([]File{files[0]}, run2); err != nil {
		t.Fatalf("UpsertBatch run2: %v", err)
	}
	if err := idx.Sweep(run2); err != nil {
		t.Fatalf("Sweep: %v", err)
	}

	if _, ok, err := idx.Stat("a.txt"); err != nil || !ok {
		t.Fatalf("a.txt should survive the sweep: ok=%v err=%v", ok, err)
	}
	if _, ok, err := idx.Stat("b.txt"); err != nil || ok {
		t.Fatalf("b.txt should have been swept: ok=%v err=%v", ok, err)
	}
}

func TestReconcileEndToEnd(t *testing.T) {
	idx := openTest(t)

	run1 := mustRun(t, idx)
	initial := []File{
		{Path: "keep.txt", Name: "keep.txt", Dir: "", Ext: "txt", Size: 1},
		{Path: "gone.txt", Name: "gone.txt", Dir: "", Ext: "txt", Size: 1},
	}
	if err := idx.Reconcile(initial, run1); err != nil {
		t.Fatalf("Reconcile initial: %v", err)
	}

	run2 := mustRun(t, idx)
	updated := []File{
		{Path: "keep.txt", Name: "keep.txt", Dir: "", Ext: "txt", Size: 999},
	}
	if err := idx.Reconcile(updated, run2); err != nil {
		t.Fatalf("Reconcile updated: %v", err)
	}

	got, ok, err := idx.Stat("keep.txt")
	if err != nil || !ok {
		t.Fatalf("keep.txt should still exist: ok=%v err=%v", ok, err)
	}
	if got.Size != 999 {
		t.Fatalf("keep.txt should have the updated size, got %d", got.Size)
	}
	if _, ok, err := idx.Stat("gone.txt"); err != nil || ok {
		t.Fatalf("gone.txt should have been reconciled away: ok=%v err=%v", ok, err)
	}
}

func TestUpsertAndDeleteSingleFile(t *testing.T) {
	idx := openTest(t)

	run := mustRun(t, idx)
	f := File{Path: "note.md", Name: "note.md", Dir: "", Ext: "md"}
	if err := idx.Upsert(f, run); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if _, ok, err := idx.Stat("note.md"); err != nil || !ok {
		t.Fatalf("note.md should exist after Upsert: ok=%v err=%v", ok, err)
	}

	if err := idx.Delete("note.md"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok, err := idx.Stat("note.md"); err != nil || ok {
		t.Fatalf("note.md should be gone after Delete: ok=%v err=%v", ok, err)
	}
}
