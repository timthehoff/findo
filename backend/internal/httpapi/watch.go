package httpapi

import (
	"context"
	"log"

	"github.com/thoff/findo/backend/internal/index"
	"github.com/thoff/findo/backend/internal/smbclient"
)

// Watch runs the change-notify listener until ctx is cancelled, keeping
// the index in sync between full crawls: individual add/modify/remove/
// rename events are applied directly, and whenever the notify stream
// can't guarantee continuity (smbclient.ErrNeedsResync — a dropped-changes
// signal from the NAS or a reconnect), a full Crawl is triggered to catch
// up on whatever might have been missed.
func (s *Server) Watch(ctx context.Context) {
	events, errs := s.smb.Watch(ctx, smbclient.DefaultChangeFilter)
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				return
			}
			s.applyChangeEvent(ev)
		case err, ok := <-errs:
			if !ok {
				return
			}
			log.Printf("change notify: %v", err)
			s.resync()
		case <-ctx.Done():
			return
		}
	}
}

// resync triggers a full crawl to recover from a gap in the notify
// stream, sharing the same crawling guard as the manual /reindex endpoint.
func (s *Server) resync() {
	if !s.crawling.CompareAndSwap(false, true) {
		// A crawl is already in flight (manual reindex or an earlier
		// resync) — it'll reconcile the index against current state
		// regardless of what triggered it, so this resync need is
		// already covered.
		return
	}
	go func() {
		defer s.crawling.Store(false)
		if err := s.Crawl(); err != nil {
			log.Printf("resync crawl failed: %v", err)
		}
	}()
}

func (s *Server) applyChangeEvent(ev smbclient.ChangeEvent) {
	switch ev.Kind {
	case smbclient.ChangeRemoved:
		if err := s.idx.Delete(ev.Path); err != nil {
			log.Printf("change notify: delete %q: %v", ev.Path, err)
		}
	case smbclient.ChangeRenamed:
		if err := s.idx.Delete(ev.OldPath); err != nil {
			log.Printf("change notify: delete %q (renamed from): %v", ev.OldPath, err)
		}
		s.upsertPath(ev.Path)
	case smbclient.ChangeUpserted:
		s.upsertPath(ev.Path)
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
func (s *Server) upsertPath(path string) {
	entry, ok, err := s.smb.Stat(path)
	if err != nil {
		log.Printf("change notify: stat %q: %v", path, err)
		return
	}
	if !ok {
		// Already gone by the time we got to it (e.g. created then
		// deleted in quick succession) — nothing to index.
		return
	}

	dir, ext := index.SplitPath(entry.Path)
	f := index.File{
		Path:    entry.Path,
		Name:    entry.Name,
		Dir:     dir,
		Ext:     ext,
		IsDir:   entry.IsDir,
		Size:    entry.Size,
		ModTime: entry.ModTime,
	}
	if err := s.idx.Upsert(f, 0); err != nil {
		log.Printf("change notify: upsert %q: %v", path, err)
	}
}
