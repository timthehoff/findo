package index

import (
	"errors"
	"testing"
)

var textExts = []string{"txt", "md", "go"}

// optedInVolume creates a volume with content indexing on.
func optedInVolume(t *testing.T, idx *Index, name string) int64 {
	t.Helper()
	v, err := idx.CreateVolume(name, "h", "s", "u", []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := idx.SetVolumeContentIndex(v.ID, true); err != nil {
		t.Fatal(err)
	}
	return v.ID
}

func seedFiles(t *testing.T, idx *Index, vol int64, files ...File) {
	t.Helper()
	run, err := idx.StartCrawlRun(vol, "manual")
	if err != nil {
		t.Fatal(err)
	}
	for i := range files {
		files[i].VolumeID = vol
		if files[i].Name == "" {
			_, files[i].Ext = SplitPath(files[i].Path)
			files[i].Name = files[i].Path
		}
	}
	if err := idx.UpsertBatch(files, run); err != nil {
		t.Fatal(err)
	}
}

func pendingPaths(t *testing.T, idx *Index, vols []int64, max int64) []string {
	t.Helper()
	got, err := idx.PendingContent(vols, textExts, max, 100)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, p := range got {
		out = append(out, p.Path)
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestPendingContentSelectsNewAndChangedFilesOnly(t *testing.T) {
	idx := openTest(t)
	vol := optedInVolume(t, idx, "nas")
	seedFiles(t, idx, vol,
		File{Path: "a.txt", Size: 10, ModTime: 100},
		File{Path: "b.md", Size: 20, ModTime: 100},
		File{Path: "c.png", Size: 30, ModTime: 100},                                 // no extractor
		File{Path: "docs", Name: "docs", IsDir: true},                               // directory
		File{Path: ".git/config.txt", Name: "config.txt", Ext: "txt", Size: 5},      // inside a dot-directory
		File{Path: ".hidden.txt", Name: ".hidden.txt", Ext: "txt", Size: 5},         // dotfile
		File{Path: "UPPER.TXT", Name: "UPPER.TXT", Ext: "TXT", Size: 7, ModTime: 1}, // extension case
	)

	if got, want := pendingPaths(t, idx, []int64{vol}, 1<<30), []string{"UPPER.TXT", "a.txt", "b.md"}; !equalStrings(got, want) {
		t.Fatalf("initially pending = %v, want %v", got, want)
	}

	if err := idx.SaveContent(vol, "a.txt", 10, 100, []string{"hello"}); err != nil {
		t.Fatal(err)
	}
	if got := pendingPaths(t, idx, []int64{vol}, 1<<30); equalStrings(got, nil) || len(got) != 2 {
		t.Fatalf("after indexing a.txt, pending = %v, want 2 files", got)
	}

	// A changed mtime or size puts an indexed file back in the queue.
	seedFiles(t, idx, vol, File{Path: "a.txt", Size: 10, ModTime: 200})
	if got := pendingPaths(t, idx, []int64{vol}, 1<<30); len(got) != 3 {
		t.Fatalf("after an mtime change, pending = %v, want 3", got)
	}
	if err := idx.SaveContent(vol, "a.txt", 10, 200, []string{"hello"}); err != nil {
		t.Fatal(err)
	}
	seedFiles(t, idx, vol, File{Path: "a.txt", Size: 11, ModTime: 200})
	if got := pendingPaths(t, idx, []int64{vol}, 1<<30); len(got) != 3 {
		t.Fatalf("after a size change, pending = %v, want 3", got)
	}
}

func TestPendingContentLimitsToTheGivenVolumes(t *testing.T) {
	idx := openTest(t)
	a, b := optedInVolume(t, idx, "a"), optedInVolume(t, idx, "b")
	seedFiles(t, idx, a, File{Path: "one.txt", Size: 1})
	seedFiles(t, idx, b, File{Path: "two.txt", Size: 1})

	if got := pendingPaths(t, idx, []int64{b}, 1<<30); !equalStrings(got, []string{"two.txt"}) {
		t.Fatalf("only volume b: %v", got)
	}
	if got := pendingPaths(t, idx, nil, 1<<30); got != nil {
		t.Fatalf("no volumes should yield nothing, got %v", got)
	}
	if got, _ := idx.PendingContent([]int64{a, b}, textExts, 1<<30, 1); len(got) != 1 {
		t.Fatalf("limit 1 returned %d", len(got))
	}
}

func TestSkippedTooLargeRetriesOnlyWhenTheLimitGrows(t *testing.T) {
	idx := openTest(t)
	vol := optedInVolume(t, idx, "nas")
	seedFiles(t, idx, vol, File{Path: "big.txt", Size: 5000, ModTime: 1}, File{Path: "bad.txt", Size: 10, ModTime: 1}, File{Path: "bin.txt", Size: 10, ModTime: 1})

	if err := idx.SaveContentSkip(vol, "big.txt", 5000, 1, ContentSkipped, "too_large"); err != nil {
		t.Fatal(err)
	}
	if err := idx.SaveContentSkip(vol, "bad.txt", 10, 1, ContentFailed, "boom"); err != nil {
		t.Fatal(err)
	}
	if err := idx.SaveContentSkip(vol, "bin.txt", 10, 1, ContentSkipped, "unsupported"); err != nil {
		t.Fatal(err)
	}

	if got := pendingPaths(t, idx, []int64{vol}, 1000); got != nil {
		t.Fatalf("with the limit still below the file's size, pending = %v", got)
	}
	if got := pendingPaths(t, idx, []int64{vol}, 10000); !equalStrings(got, []string{"big.txt"}) {
		t.Fatalf("after raising the limit, pending = %v, want only big.txt (failed and unsupported stay put)", got)
	}

	n, err := idx.RetryFailedContent(vol)
	if err != nil || n != 1 {
		t.Fatalf("RetryFailedContent = %d, %v; want 1", n, err)
	}
	if got := pendingPaths(t, idx, []int64{vol}, 1000); !equalStrings(got, []string{"bad.txt"}) {
		t.Fatalf("after retry, pending = %v, want bad.txt", got)
	}
}

func chunkCount(t *testing.T, idx *Index, vol int64, path string) int {
	t.Helper()
	var n int
	if err := idx.db.QueryRow(`SELECT count(*) FROM chunks WHERE volume_id = $1 AND path = $2`, vol, path).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestSaveContentReplacesChunksAndIsSearchable(t *testing.T) {
	idx := openTest(t)
	vol := optedInVolume(t, idx, "nas")
	seedFiles(t, idx, vol, File{Path: "notes.txt", Size: 10, ModTime: 1})

	if err := idx.SaveContent(vol, "notes.txt", 10, 1, []string{"quarterly budget forecast", "second chunk", "third chunk"}); err != nil {
		t.Fatal(err)
	}
	if n := chunkCount(t, idx, vol, "notes.txt"); n != 3 {
		t.Fatalf("chunks = %d, want 3", n)
	}
	var hits int
	// The generated tsvector stems, so "budgets" finds "budget".
	if err := idx.db.QueryRow(`SELECT count(*) FROM chunks WHERE tsv @@ plainto_tsquery('english', 'budgets')`).Scan(&hits); err != nil || hits != 1 {
		t.Fatalf("full-text hits = %d, err = %v; want 1", hits, err)
	}

	if err := idx.SaveContent(vol, "notes.txt", 12, 2, []string{"rewritten"}); err != nil {
		t.Fatal(err)
	}
	if n := chunkCount(t, idx, vol, "notes.txt"); n != 1 {
		t.Fatalf("after rewrite chunks = %d, want 1 (old chunks replaced)", n)
	}
	var count int
	var size int64
	if err := idx.db.QueryRow(`SELECT chunk_count, size FROM content_state WHERE volume_id = $1 AND path = 'notes.txt'`, vol).Scan(&count, &size); err != nil || count != 1 || size != 12 {
		t.Fatalf("state = chunk_count %d size %d err %v", count, size, err)
	}

	// Skipping a file later drops its old text.
	if err := idx.SaveContentSkip(vol, "notes.txt", 13, 3, ContentSkipped, "no_text"); err != nil {
		t.Fatal(err)
	}
	if n := chunkCount(t, idx, vol, "notes.txt"); n != 0 {
		t.Fatalf("a skipped file kept %d chunks", n)
	}
}

func TestSaveContentForAMissingFileIsErrFileGone(t *testing.T) {
	idx := openTest(t)
	vol := optedInVolume(t, idx, "nas")
	if err := idx.SaveContent(vol, "ghost.txt", 1, 1, []string{"x"}); !errors.Is(err, ErrFileGone) {
		t.Fatalf("SaveContent for an unindexed file = %v, want ErrFileGone", err)
	}
	if err := idx.SaveContentSkip(vol, "ghost.txt", 1, 1, ContentFailed, "x"); !errors.Is(err, ErrFileGone) {
		t.Fatalf("SaveContentSkip for an unindexed file = %v, want ErrFileGone", err)
	}
	if err := idx.SaveContentSkip(vol, "ghost.txt", 1, 1, "done", "x"); err == nil {
		t.Fatal("an invalid status must be rejected")
	}
}

func TestDeletingAFileRemovesItsTextAndState(t *testing.T) {
	idx := openTest(t)
	vol := optedInVolume(t, idx, "nas")
	seedFiles(t, idx, vol, File{Path: "keep.txt", Size: 1, ModTime: 1}, File{Path: "gone.txt", Size: 1, ModTime: 1}, File{Path: "swept.txt", Size: 1, ModTime: 1})
	for _, p := range []string{"keep.txt", "gone.txt", "swept.txt"} {
		if err := idx.SaveContent(vol, p, 1, 1, []string{"text of " + p}); err != nil {
			t.Fatal(err)
		}
	}

	if err := idx.Delete(vol, "gone.txt"); err != nil {
		t.Fatal(err)
	}
	// A crawl that no longer sees swept.txt sweeps it.
	run, _ := idx.StartCrawlRun(vol, "manual")
	if err := idx.UpsertBatch([]File{{VolumeID: vol, Path: "keep.txt", Name: "keep.txt", Ext: "txt", Size: 1, ModTime: 1}}, run); err != nil {
		t.Fatal(err)
	}
	if _, err := idx.Sweep(vol, run); err != nil {
		t.Fatal(err)
	}

	for p, want := range map[string]int{"keep.txt": 1, "gone.txt": 0, "swept.txt": 0} {
		if n := chunkCount(t, idx, vol, p); n != want {
			t.Errorf("%s: chunks = %d, want %d", p, n, want)
		}
		var states int
		idx.db.QueryRow(`SELECT count(*) FROM content_state WHERE volume_id = $1 AND path = $2`, vol, p).Scan(&states)
		if states != want {
			t.Errorf("%s: content_state rows = %d, want %d", p, states, want)
		}
	}
}

func TestOptingOutPurgesOnlyThatVolumesText(t *testing.T) {
	idx := openTest(t)
	a, b := optedInVolume(t, idx, "a"), optedInVolume(t, idx, "b")
	seedFiles(t, idx, a, File{Path: "x.txt", Size: 1, ModTime: 1})
	seedFiles(t, idx, b, File{Path: "y.txt", Size: 1, ModTime: 1})
	idx.SaveContent(a, "x.txt", 1, 1, []string{"alpha"})
	idx.SaveContent(b, "y.txt", 1, 1, []string{"beta"})

	if _, err := idx.SetVolumeContentIndex(a, false); err != nil {
		t.Fatal(err)
	}
	if chunkCount(t, idx, a, "x.txt") != 0 || chunkCount(t, idx, b, "y.txt") != 1 {
		t.Fatal("opting out must purge volume a's chunks and leave volume b's alone")
	}
	var states int
	idx.db.QueryRow(`SELECT count(*) FROM content_state WHERE volume_id = $1`, a).Scan(&states)
	if states != 0 {
		t.Fatalf("volume a kept %d content_state rows", states)
	}
	// The files themselves (names) stay indexed.
	if _, ok, _ := idx.Stat(a, "x.txt"); !ok {
		t.Fatal("opting out removed the file's name from the index")
	}
	// Opting back in re-queues everything.
	if _, err := idx.SetVolumeContentIndex(a, true); err != nil {
		t.Fatal(err)
	}
	if got := pendingPaths(t, idx, []int64{a}, 1<<30); !equalStrings(got, []string{"x.txt"}) {
		t.Fatalf("after opting back in, pending = %v", got)
	}
}

func TestContentProgressCountsAndReasons(t *testing.T) {
	idx := openTest(t)
	vol := optedInVolume(t, idx, "nas")
	off, err := idx.CreateVolume("off", "h", "s", "u", []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	seedFiles(t, idx, off.ID, File{Path: "ignored.txt", Size: 1})
	seedFiles(t, idx, vol,
		File{Path: "done.txt", Size: 1, ModTime: 1},
		File{Path: "big1.txt", Size: 5000, ModTime: 1},
		File{Path: "big2.txt", Size: 6000, ModTime: 1},
		File{Path: "bin.txt", Size: 1, ModTime: 1},
		File{Path: "bad.txt", Size: 1, ModTime: 1},
		File{Path: "new.txt", Size: 1, ModTime: 1},
		File{Path: "stale.txt", Size: 1, ModTime: 9},
		File{Path: "pic.png", Size: 1, ModTime: 1},
	)
	idx.SaveContent(vol, "done.txt", 1, 1, []string{"x"})
	idx.SaveContentSkip(vol, "big1.txt", 5000, 1, ContentSkipped, "too_large")
	idx.SaveContentSkip(vol, "big2.txt", 6000, 1, ContentSkipped, "too_large")
	idx.SaveContentSkip(vol, "bin.txt", 1, 1, ContentSkipped, "unsupported")
	idx.SaveContentSkip(vol, "bad.txt", 1, 1, ContentFailed, "read error")
	idx.SaveContent(vol, "stale.txt", 1, 1, []string{"old"}) // file's mtime is now 9: pending again

	got, err := idx.ContentProgress(textExts, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].VolumeID != vol {
		t.Fatalf("progress should cover only the opted-in volume, got %+v", got)
	}
	p := got[0]
	if p.Eligible != 7 || p.Done != 1 || p.Skipped != 3 || p.Failed != 1 || p.Pending != 2 {
		t.Fatalf("progress = %+v; want eligible 7, done 1, skipped 3, failed 1, pending 2 (new + stale)", p)
	}
	want := map[string]int{"skipped/too_large": 2, "skipped/unsupported": 1, "failed/read error": 1}
	for _, r := range p.Reasons {
		if want[r.Status+"/"+r.Reason] != r.Count {
			t.Errorf("reason %+v unexpected", r)
		}
		delete(want, r.Status+"/"+r.Reason)
	}
	if len(want) != 0 {
		t.Errorf("missing reasons: %v", want)
	}

	// With a higher limit the too-large files are queued, not skipped.
	raised, _ := idx.ContentProgress(textExts, 1<<20)
	if raised[0].Skipped != 1 || raised[0].Pending != 4 {
		t.Fatalf("with a raised limit: %+v; want skipped 1, pending 4", raised[0])
	}
}

func TestContentProgressForAnOptedInVolumeWithNothingYet(t *testing.T) {
	idx := openTest(t)
	vol := optedInVolume(t, idx, "empty")
	got, err := idx.ContentProgress(textExts, 1000)
	if err != nil || len(got) != 1 || got[0].VolumeID != vol || got[0].Eligible != 0 || got[0].Reasons == nil {
		t.Fatalf("got %+v, %v; want one empty entry with a non-nil reasons list", got, err)
	}
}

func TestContentFor(t *testing.T) {
	idx := openTest(t)
	vol := optedInVolume(t, idx, "nas")
	seedFiles(t, idx, vol, File{Path: "a.txt", Size: 1, ModTime: 1}, File{Path: "b.txt", Size: 1, ModTime: 1}, File{Path: "c.txt", Size: 1, ModTime: 1})
	idx.SaveContent(vol, "a.txt", 1, 1, []string{"first", "second", "third"})
	idx.SaveContentSkip(vol, "b.txt", 1, 1, ContentSkipped, "no_text")

	got, ok, err := idx.ContentFor(vol, "a.txt")
	if err != nil || !ok || got.Status != ContentDone || len(got.Chunks) != 3 || got.Chunks[0] != "first" || got.Chunks[2] != "third" {
		t.Fatalf("a.txt = %+v ok=%v err=%v; want 3 chunks in order", got, ok, err)
	}
	got, ok, err = idx.ContentFor(vol, "b.txt")
	if err != nil || !ok || got.Status != ContentSkipped || got.Reason != "no_text" || len(got.Chunks) != 0 {
		t.Fatalf("b.txt = %+v ok=%v err=%v", got, ok, err)
	}
	if _, ok, err := idx.ContentFor(vol, "c.txt"); ok || err != nil {
		t.Fatalf("never-attempted file: ok=%v err=%v, want not found", ok, err)
	}
}
