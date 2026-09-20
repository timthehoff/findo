package httpapi

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/thoff/findo/backend/internal/crypto"
	"github.com/thoff/findo/backend/internal/index"
	"github.com/thoff/findo/backend/internal/smbclient"
)

// newTestServer returns a Server backed by a temp-file index and one
// registered volume whose SMB session is the returned fakeSMB — a fixed
// runtime, not a real connection, so tests can drive Crawl/Watch/handlers
// deterministically without the background goroutines StartVolume would
// normally kick off.
func newTestServer(t *testing.T) (*Server, *fakeSMB, int64) {
	t.Helper()
	idx, err := index.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("index.Open: %v", err)
	}
	t.Cleanup(func() { idx.Close() })

	key := make([]byte, crypto.KeySize)
	srv := NewServer(idx, key)

	passwordEnc, err := crypto.Encrypt(key, []byte("pass"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	vol, err := idx.CreateVolume("test volume", "host", "share", "user", passwordEnc)
	if err != nil {
		t.Fatalf("CreateVolume: %v", err)
	}

	smb := newFakeSMB()
	_, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	srv.registerVolumeRuntime(vol.ID, smb, cancel)

	return srv, smb, vol.ID
}

func TestCrawlIndexesDiscoveredFiles(t *testing.T) {
	srv, smb, volID := newTestServer(t)
	smb.set(smbclient.Entry{Path: "docs/a.txt", Name: "a.txt", Size: 10})
	smb.set(smbclient.Entry{Path: "docs/b.txt", Name: "b.txt", Size: 20})

	if err := srv.Crawl(volID); err != nil {
		t.Fatalf("Crawl: %v", err)
	}

	files, err := srv.idx.Search(volID, "", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 indexed files, got %d: %+v", len(files), files)
	}
}

func TestCrawlSweepsFilesNoLongerOnTheShare(t *testing.T) {
	srv, smb, volID := newTestServer(t)
	smb.set(smbclient.Entry{Path: "keep.txt", Name: "keep.txt"})
	smb.set(smbclient.Entry{Path: "gone.txt", Name: "gone.txt"})
	if err := srv.Crawl(volID); err != nil {
		t.Fatalf("Crawl: %v", err)
	}

	smb.remove("gone.txt")
	if err := srv.Crawl(volID); err != nil {
		t.Fatalf("second Crawl: %v", err)
	}

	if _, ok, _ := srv.idx.Stat(volID, "keep.txt"); !ok {
		t.Fatalf("keep.txt should still be indexed")
	}
	if _, ok, _ := srv.idx.Stat(volID, "gone.txt"); ok {
		t.Fatalf("gone.txt should have been swept")
	}
}

func TestCrawlSkipsSweepOnWalkError(t *testing.T) {
	srv, smb, volID := newTestServer(t)
	smb.set(smbclient.Entry{Path: "keep.txt", Name: "keep.txt"})
	if err := srv.Crawl(volID); err != nil {
		t.Fatalf("Crawl: %v", err)
	}

	// Simulate a directory that failed to list this run: keep.txt is
	// gone from what Walk reports, but Crawl should still leave it
	// indexed rather than sweep it away, since the failure means we
	// can't tell a genuine deletion from a subtree we just couldn't see.
	smb.remove("keep.txt")
	smb.walkErr = errFakeListing

	if err := srv.Crawl(volID); err == nil {
		t.Fatalf("expected Crawl to surface the walk error")
	}
	if _, ok, _ := srv.idx.Stat(volID, "keep.txt"); !ok {
		t.Fatalf("keep.txt should survive a crawl with a walk error, not be swept")
	}
}
