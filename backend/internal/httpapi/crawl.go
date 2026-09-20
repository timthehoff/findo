package httpapi

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/thoff/findo/backend/internal/index"
	"github.com/thoff/findo/backend/internal/smbclient"
)

// crawlWriteBatch bounds how many discovered files accumulate before a
// batch is upserted, so a crawl streams into SQLite as directories are
// listed instead of buffering the whole tree in memory first.
const crawlWriteBatch = 2000

// periodicCrawlInterval is the safety-net full-crawl cadence alongside each
// volume's change-notify listener — not config-driven, since there's no
// reason yet for a deployment to want it tuned per volume.
const periodicCrawlInterval = 24 * time.Hour

// Crawl walks volumeID's SMB share and reconciles the index against what it
// finds. Directory listings run concurrently (smbclient.Client.Walk) and
// discovered files stream into SQLite via Index.StreamUpsert as they're
// found, rather than being collected into one big slice first.
//
// Walk is best-effort: a directory that fails to list doesn't abort the
// crawl, it's recorded and skipped. But if anything failed to list, the
// index isn't swept afterward — a sweep can't tell a genuine deletion from
// a subtree we simply couldn't observe this run, so stale rows are left in
// place rather than risking indexing data loss.
func (s *Server) Crawl(volumeID int64) error {
	rt := s.volumeRuntime(volumeID)
	if rt == nil {
		return fmt.Errorf("volume %d is not connected", volumeID)
	}

	runID, err := s.idx.StartCrawlRun(volumeID)
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

	walkErr := rt.smb.Walk("", func(e smbclient.Entry) {
		dir, ext := index.SplitPath(e.Path)
		entries <- index.File{
			VolumeID: volumeID,
			Path:     e.Path,
			Name:     e.Name,
			Dir:      dir,
			Ext:      ext,
			IsDir:    e.IsDir,
			Size:     e.Size,
			ModTime:  e.ModTime,
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
		runErr = s.idx.Sweep(volumeID, runID)
	}

	if err := s.idx.FinishCrawlRun(runID, runErr); err != nil {
		log.Printf("volume %d: record crawl run finish: %v", volumeID, err)
	}

	if runErr != nil {
		log.Printf("volume %d: crawl finished with errors: %d entries indexed, error: %v", volumeID, written, runErr)
	} else {
		log.Printf("volume %d: crawl complete: %d entries indexed", volumeID, written)
	}
	return runErr
}

// PeriodicCrawl runs a full Crawl for volumeID on a fixed interval until
// ctx is cancelled — a safety net alongside the change-notify listener
// (Watch), since a missed or misread notification is hard to fully rule
// out over a long enough time.
func (s *Server) PeriodicCrawl(ctx context.Context, volumeID int64, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.resync(volumeID)
		case <-ctx.Done():
			return
		}
	}
}
