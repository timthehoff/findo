package httpapi

import (
	"context"
	"log"
	"time"

	"github.com/thoff/findo/backend/internal/index"
	"github.com/thoff/findo/backend/internal/smbclient"
)

// crawlWriteBatch bounds how many discovered files accumulate before a
// batch is upserted, so a crawl streams into SQLite as directories are
// listed instead of buffering the whole tree in memory first.
const crawlWriteBatch = 2000

// Crawl walks the SMB share and reconciles the index against what it finds.
// Directory listings run concurrently (smbclient.Client.Walk) and
// discovered files stream into SQLite via Index.StreamUpsert as they're
// found, rather than being collected into one big slice first.
//
// Walk is best-effort: a directory that fails to list doesn't abort the
// crawl, it's recorded and skipped. But if anything failed to list, the
// index isn't swept afterward — a sweep can't tell a genuine deletion from
// a subtree we simply couldn't observe this run, so stale rows are left in
// place rather than risking indexing data loss.
func (s *Server) Crawl() error {
	runID, err := s.idx.StartCrawlRun()
	if err != nil {
		return err
	}

	entries := make(chan index.File, crawlWriteBatch)
	writerDone := make(chan struct{})
	var written int
	var writeErr error
	go func() {
		defer close(writerDone)
		written, writeErr = s.idx.StreamUpsert(entries, runID, crawlWriteBatch)
	}()

	walkErr := s.smb.Walk("", func(e smbclient.Entry) {
		dir, ext := index.SplitPath(e.Path)
		entries <- index.File{
			Path:    e.Path,
			Name:    e.Name,
			Dir:     dir,
			Ext:     ext,
			IsDir:   e.IsDir,
			Size:    e.Size,
			ModTime: e.ModTime,
		}
	})
	close(entries)
	<-writerDone

	var runErr error
	switch {
	case writeErr != nil:
		runErr = writeErr
	case walkErr != nil:
		runErr = walkErr
	default:
		runErr = s.idx.Sweep(runID)
	}

	if err := s.idx.FinishCrawlRun(runID, runErr); err != nil {
		log.Printf("record crawl run finish: %v", err)
	}

	if runErr != nil {
		log.Printf("crawl finished with errors: %d entries indexed, error: %v", written, runErr)
	} else {
		log.Printf("crawl complete: %d entries indexed", written)
	}
	return runErr
}

// PeriodicCrawl runs a full Crawl on a fixed interval until ctx is
// cancelled — a safety net alongside the change-notify listener (Watch),
// since a missed or misread notification is hard to fully rule out over a
// long enough time. Shares Watch's resync path, so it won't pile a crawl
// on top of one already running.
func (s *Server) PeriodicCrawl(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.resync()
		case <-ctx.Done():
			return
		}
	}
}
