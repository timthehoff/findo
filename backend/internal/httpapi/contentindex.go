package httpapi

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/thoff/findo/backend/internal/extract"
	"github.com/thoff/findo/backend/internal/index"
	"github.com/thoff/findo/backend/internal/smbclient"
)

const (
	// contentBatch is how many pending files one pass takes. Settings are
	// re-read between passes, so edits (including pause) apply within a batch.
	contentBatch          = 50
	defaultContentTimeout = 60 * time.Second // per file, reading and extracting
	defaultContentIdle    = 30 * time.Second // how long to sleep when the queue is empty
	maxReasonLen          = 200
)

// kickContent wakes the content indexer early, e.g. after a crawl finishes or
// a setting changes. It never blocks.
func (s *Server) kickContent() {
	select {
	case s.contentKick <- struct{}{}:
	default:
	}
}

// RunContentIndexer extracts text from files in opted-in volumes until ctx is
// cancelled. It is separate from the metadata crawl: the crawl only records
// which files exist; this works through whichever of them are new or changed,
// within the limits in the content settings.
func (s *Server) RunContentIndexer(ctx context.Context) {
	for {
		worked := s.contentPass(ctx)
		if ctx.Err() != nil {
			return
		}
		if worked {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-s.contentKick:
		case <-time.After(s.contentIdle):
		}
	}
}

// indexableVolumeIDs lists volumes worth reading from right now: opted in,
// enabled, connected, and not in the middle of a crawl (which is rewriting
// their file list).
func (s *Server) indexableVolumeIDs() []int64 {
	vols, err := s.idx.ListVolumes()
	if err != nil {
		log.Printf("content: list volumes: %v", err)
		return nil
	}
	var ids []int64
	for _, v := range vols {
		if !v.Enabled || !v.ContentIndex {
			continue
		}
		if rt := s.volumeRuntime(v.ID); rt != nil && !rt.crawling.Load() {
			ids = append(ids, v.ID)
		}
	}
	return ids
}

// contentPass indexes one batch and reports whether it made progress.
func (s *Server) contentPass(ctx context.Context) bool {
	cs, err := s.idx.ContentSettings()
	if err != nil {
		log.Printf("content: load settings: %v", err)
		return false
	}
	if cs.Paused {
		return false
	}
	ids := s.indexableVolumeIDs()
	if len(ids) == 0 {
		return false
	}
	pending, err := s.idx.PendingContent(ids, extract.Supported(), cs.MaxFileBytes, contentBatch)
	if err != nil {
		log.Printf("content: find pending files: %v", err)
		return false
	}

	var recorded atomic.Int64
	jobs := make(chan index.PendingFile)
	var wg sync.WaitGroup
	for i := 0; i < cs.Workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for f := range jobs {
				if s.indexOne(ctx, cs, f) {
					recorded.Add(1)
				}
				if cs.DelayMS > 0 {
					select {
					case <-time.After(time.Duration(cs.DelayMS) * time.Millisecond):
					case <-ctx.Done():
					}
				}
			}
		}()
	}
feed:
	for _, f := range pending {
		select {
		case jobs <- f:
		case <-ctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()

	if n := recorded.Load(); n > 0 {
		log.Printf("content: processed %d files", n)
	}
	return recorded.Load() > 0
}

// indexOne extracts one file and records the outcome, reporting whether
// something was recorded (a file that vanished mid-pass records nothing).
func (s *Server) indexOne(ctx context.Context, cs index.ContentSettings, f index.PendingFile) bool {
	rt := s.volumeRuntime(f.VolumeID)
	if rt == nil {
		return false
	}
	if f.Size > cs.MaxFileBytes {
		return s.recordContent(s.idx.SaveContentSkip(f.VolumeID, f.Path, f.Size, f.ModTime, index.ContentSkipped, "too_large"), f)
	}

	text, err := s.extractFile(ctx, rt, cs, f)
	if ctx.Err() != nil {
		return false // shutting down: don't record a failure caused by that
	}
	switch {
	case errors.Is(err, extract.ErrEncrypted):
		return s.recordContent(s.idx.SaveContentSkip(f.VolumeID, f.Path, f.Size, f.ModTime, index.ContentSkipped, "encrypted"), f)
	case errors.Is(err, extract.ErrBinary), errors.Is(err, extract.ErrUnsupported):
		return s.recordContent(s.idx.SaveContentSkip(f.VolumeID, f.Path, f.Size, f.ModTime, index.ContentSkipped, "unsupported"), f)
	case err != nil:
		return s.recordContent(s.idx.SaveContentSkip(f.VolumeID, f.Path, f.Size, f.ModTime, index.ContentFailed, truncateReason(err.Error())), f)
	case strings.TrimSpace(text) == "":
		return s.recordContent(s.idx.SaveContentSkip(f.VolumeID, f.Path, f.Size, f.ModTime, index.ContentSkipped, "no_text"), f)
	}
	return s.recordContent(s.idx.SaveContent(f.VolumeID, f.Path, f.Size, f.ModTime, extract.Chunk(text, extract.ChunkSize)), f)
}

func (s *Server) recordContent(err error, f index.PendingFile) bool {
	if err == nil {
		return true
	}
	if !errors.Is(err, index.ErrFileGone) {
		log.Printf("content: record %d:%s: %v", f.VolumeID, f.Path, err)
	}
	return false
}

// extractFile opens, reads and extracts one file, giving up after
// s.contentTimeout. Extractors run on malformed input from a network share, so
// a panic is caught. Nothing here may block the caller past the deadline: the
// SMB library's calls ignore our context and serialize on a per-connection
// lock, so after the volume's session is replaced under us an Open, a read or
// even a Close can hang forever (this once froze the whole indexer for days).
// The work therefore runs in its own goroutine, and Close is only ever called
// from goroutines nobody waits for.
func (s *Server) extractFile(ctx context.Context, rt *volumeRuntime, cs index.ContentSettings, f index.PendingFile) (string, error) {
	type result struct {
		text string
		err  error
	}
	// One deadline for the whole file: it interrupts a stuck PDF engine through ctx.
	ctx, cancel := context.WithTimeout(ctx, s.contentTimeout)
	defer cancel()

	var (
		mu        sync.Mutex
		file      smbclient.File
		abandoned bool
	)
	closeFile := func() {
		mu.Lock()
		fl := file
		abandoned = true
		mu.Unlock()
		if fl != nil {
			go fl.Close() // may block on a dead connection's lock; nobody waits for it
		}
	}

	done := make(chan result, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- result{err: fmt.Errorf("extractor panic: %v", r)}
			}
		}()
		fl, _, err := rt.smb.Open(f.Path)
		if err != nil {
			done <- result{err: err}
			return
		}
		mu.Lock()
		if abandoned {
			mu.Unlock()
			go fl.Close()
			return
		}
		file = fl
		mu.Unlock()
		text, err := extract.Extract(ctx, strings.ToLower(f.Ext), fl, f.Size, cs.MaxTextBytes)
		done <- result{text, err}
	}()

	select {
	case r := <-done:
		closeFile()
		return r.text, r.err
	case <-ctx.Done():
		closeFile() // unblocks readers that only stall on I/O
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", errors.New("timed out reading the file")
		}
		return "", ctx.Err()
	}
}

func truncateReason(s string) string {
	if utf8.RuneCountInString(s) <= maxReasonLen {
		return s
	}
	return string([]rune(s)[:maxReasonLen])
}

func (s *Server) handleContentStatus(w http.ResponseWriter, r *http.Request) {
	cs, err := s.idx.ContentSettings()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	scope, err := s.scope(principalFrom(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	progress, err := s.idx.ContentProgress(scope, extract.Supported(), cs.MaxFileBytes)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"paused": cs.Paused, "volumes": progress})
}

// handleContentRetry queues a volume's failed files again.
func (s *Server) handleContentRetry(w http.ResponseWriter, r *http.Request) {
	n, err := s.idx.RetryFailedContent(volumeFrom(r).ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.kickContent()
	writeJSON(w, http.StatusOK, map[string]int64{"retried": n})
}
