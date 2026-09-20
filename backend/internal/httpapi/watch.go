package httpapi

import (
	"context"
	"log"

	"github.com/thoff/findo/backend/internal/index"
	"github.com/thoff/findo/backend/internal/smbclient"
)

// Watch runs volumeID's change-notify listener until ctx is cancelled,
// keeping the index in sync between full crawls: individual add/modify/
// remove/rename events are applied directly, and whenever the notify
// stream can't guarantee continuity (smbclient.ErrNeedsResync — a
// dropped-changes signal from the NAS or a reconnect), a full Crawl is
// triggered to catch up on whatever might have been missed.
func (s *Server) Watch(ctx context.Context, volumeID int64) {
	rt := s.volumeRuntime(volumeID)
	if rt == nil {
		return
	}

	events, errs := rt.smb.Watch(ctx, smbclient.DefaultChangeFilter)
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				return
			}
			s.applyChangeEvent(volumeID, ev)
		case err, ok := <-errs:
			if !ok {
				return
			}
			log.Printf("volume %d: change notify: %v", volumeID, err)
			s.resync(volumeID)
		case <-ctx.Done():
			return
		}
	}
}

// resync triggers a full crawl to recover from a gap in volumeID's notify
// stream, sharing the same crawling guard as the manual /reindex endpoint.
func (s *Server) resync(volumeID int64) {
	if err := s.triggerCrawl(volumeID); err != nil {
		// Already crawling (manual reindex or an earlier resync) — it'll
		// reconcile the index against current state regardless of what
		// triggered it, so this resync need is already covered.
		return
	}
}

func (s *Server) applyChangeEvent(volumeID int64, ev smbclient.ChangeEvent) {
	switch ev.Kind {
	case smbclient.ChangeRemoved:
		if err := s.idx.Delete(volumeID, ev.Path); err != nil {
			log.Printf("volume %d: change notify: delete %q: %v", volumeID, ev.Path, err)
		}
	case smbclient.ChangeRenamed:
		if err := s.idx.Delete(volumeID, ev.OldPath); err != nil {
			log.Printf("volume %d: change notify: delete %q (renamed from): %v", volumeID, ev.OldPath, err)
		}
		s.upsertPath(volumeID, ev.Path)
	case smbclient.ChangeUpserted:
		s.upsertPath(volumeID, ev.Path)
	}
}

// upsertPath re-stats path and upserts the result — a change-notify event
// only carries a name and an action, not fresh metadata.
//
// Live updates are stamped with runID 0, a sentinel that never collides
// with a real crawl run (StartCrawlRun's ids are SQLite AUTOINCREMENT, so
// they start at 1 and never repeat). That keeps a live-upserted row alive
// until the next real crawl either re-confirms it — re-stamping it with
// that crawl's own run id as it walks past the file again — or genuinely
// doesn't find it anymore and sweeps it away, so a stale live update can't
// outlive the next full crawl.
func (s *Server) upsertPath(volumeID int64, path string) {
	rt := s.volumeRuntime(volumeID)
	if rt == nil {
		return
	}

	entry, ok, err := rt.smb.Stat(path)
	if err != nil {
		log.Printf("volume %d: change notify: stat %q: %v", volumeID, path, err)
		return
	}
	if !ok {
		// Already gone by the time we got to it (e.g. created then
		// deleted in quick succession) — nothing to index.
		return
	}

	dir, ext := index.SplitPath(entry.Path)
	f := index.File{
		VolumeID: volumeID,
		Path:     entry.Path,
		Name:     entry.Name,
		Dir:      dir,
		Ext:      ext,
		IsDir:    entry.IsDir,
		Size:     entry.Size,
		ModTime:  entry.ModTime,
	}
	if err := s.idx.Upsert(f, 0); err != nil {
		log.Printf("volume %d: change notify: upsert %q: %v", volumeID, path, err)
	}
}
