package httpapi

import (
	"path/filepath"
	"testing"

	"github.com/thoff/findo/backend/internal/index"
	"github.com/thoff/findo/backend/internal/smbclient"
)

func newTestServer(t *testing.T) (*Server, *fakeSMB) {
	t.Helper()
	idx, err := index.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("index.Open: %v", err)
	}
	t.Cleanup(func() { idx.Close() })

	smb := newFakeSMB()
	return NewServer(smb, idx), smb
}

func TestCrawlIndexesDiscoveredFiles(t *testing.T) {
	srv, smb := newTestServer(t)
	smb.set(smbclient.Entry{Path: "docs/a.txt", Name: "a.txt", Size: 10})
	smb.set(smbclient.Entry{Path: "docs/b.txt", Name: "b.txt", Size: 20})

	if err := srv.Crawl(); err != nil {
		t.Fatalf("Crawl: %v", err)
	}

	files, err := srv.idx.Search("", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 indexed files, got %d: %+v", len(files), files)
	}
}

func TestCrawlSweepsFilesNoLongerOnTheShare(t *testing.T) {
	srv, smb := newTestServer(t)
	smb.set(smbclient.Entry{Path: "keep.txt", Name: "keep.txt"})
	smb.set(smbclient.Entry{Path: "gone.txt", Name: "gone.txt"})
	if err := srv.Crawl(); err != nil {
		t.Fatalf("Crawl: %v", err)
	}

	smb.remove("gone.txt")
	if err := srv.Crawl(); err != nil {
		t.Fatalf("second Crawl: %v", err)
	}

	if _, ok, _ := srv.idx.Stat("keep.txt"); !ok {
		t.Fatalf("keep.txt should still be indexed")
	}
	if _, ok, _ := srv.idx.Stat("gone.txt"); ok {
		t.Fatalf("gone.txt should have been swept")
	}
}

func TestCrawlSkipsSweepOnWalkError(t *testing.T) {
	srv, smb := newTestServer(t)
	smb.set(smbclient.Entry{Path: "keep.txt", Name: "keep.txt"})
	if err := srv.Crawl(); err != nil {
		t.Fatalf("Crawl: %v", err)
	}

	// Simulate a directory that failed to list this run: keep.txt is
	// gone from what Walk reports, but Crawl should still leave it
	// indexed rather than sweep it away, since the failure means we
	// can't tell a genuine deletion from a subtree we just couldn't see.
	smb.remove("keep.txt")
	smb.walkErr = errFakeListing

	if err := srv.Crawl(); err == nil {
		t.Fatalf("expected Crawl to surface the walk error")
	}
	if _, ok, _ := srv.idx.Stat("keep.txt"); !ok {
		t.Fatalf("keep.txt should survive a crawl with a walk error, not be swept")
	}
}
