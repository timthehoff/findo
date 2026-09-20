package httpapi

import (
	"context"
	"testing"
	"time"

	"github.com/thoff/findo/backend/internal/smbclient"
)

func TestApplyChangeEventUpsertAndRemove(t *testing.T) {
	srv, smb := newTestServer(t)
	smb.set(smbclient.Entry{Path: "new.txt", Name: "new.txt", Size: 5})

	srv.applyChangeEvent(smbclient.ChangeEvent{Kind: smbclient.ChangeUpserted, Path: "new.txt"})

	f, ok, err := srv.idx.Stat("new.txt")
	if err != nil || !ok {
		t.Fatalf("expected new.txt indexed after upsert event: ok=%v err=%v", ok, err)
	}
	if f.Size != 5 {
		t.Fatalf("expected size 5, got %d", f.Size)
	}

	srv.applyChangeEvent(smbclient.ChangeEvent{Kind: smbclient.ChangeRemoved, Path: "new.txt"})
	if _, ok, _ := srv.idx.Stat("new.txt"); ok {
		t.Fatalf("expected new.txt removed after remove event")
	}
}

func TestApplyChangeEventRename(t *testing.T) {
	srv, smb := newTestServer(t)
	smb.set(smbclient.Entry{Path: "old.txt", Name: "old.txt"})
	srv.applyChangeEvent(smbclient.ChangeEvent{Kind: smbclient.ChangeUpserted, Path: "old.txt"})

	smb.remove("old.txt")
	smb.set(smbclient.Entry{Path: "new.txt", Name: "new.txt"})
	srv.applyChangeEvent(smbclient.ChangeEvent{Kind: smbclient.ChangeRenamed, OldPath: "old.txt", Path: "new.txt"})

	if _, ok, _ := srv.idx.Stat("old.txt"); ok {
		t.Fatalf("old.txt should be gone after rename")
	}
	if _, ok, _ := srv.idx.Stat("new.txt"); !ok {
		t.Fatalf("new.txt should be indexed after rename")
	}
}

func TestUpsertPathSkipsFileGoneByTheTimeItsProcessed(t *testing.T) {
	srv, _ := newTestServer(t)
	// No entry set on the fake — Stat will report ok=false, as if the
	// file was already deleted again before this event got processed.
	srv.applyChangeEvent(smbclient.ChangeEvent{Kind: smbclient.ChangeUpserted, Path: "ephemeral.txt"})

	if _, ok, _ := srv.idx.Stat("ephemeral.txt"); ok {
		t.Fatalf("nothing should have been indexed for a file that's already gone")
	}
}

func TestResyncSkipsWhenAlreadyCrawling(t *testing.T) {
	srv, _ := newTestServer(t)
	srv.crawling.Store(true)

	srv.resync()

	// resync should have declined to start a second crawl; the flag
	// should still read exactly what we set (no crawl goroutine flipped
	// it back to false on our behalf).
	time.Sleep(10 * time.Millisecond)
	if !srv.crawling.Load() {
		t.Fatalf("resync should not have touched the crawling flag when one was already in flight")
	}
}

func TestWatchAppliesEventsAndResyncsOnError(t *testing.T) {
	srv, smb := newTestServer(t)
	smb.watchEvents = make(chan smbclient.ChangeEvent, 1)
	smb.watchErrs = make(chan error, 1)
	smb.set(smbclient.Entry{Path: "live.txt", Name: "live.txt"})
	smb.set(smbclient.Entry{Path: "from-resync.txt", Name: "from-resync.txt"})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.Watch(ctx)
	}()

	smb.watchEvents <- smbclient.ChangeEvent{Kind: smbclient.ChangeUpserted, Path: "live.txt"}
	waitFor(t, func() bool {
		_, ok, _ := srv.idx.Stat("live.txt")
		return ok
	})

	smb.watchErrs <- smbclient.ErrNeedsResync
	waitFor(t, func() bool {
		_, ok, _ := srv.idx.Stat("from-resync.txt")
		return ok
	})

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("Watch did not return after ctx cancellation")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("condition not met before deadline")
}
