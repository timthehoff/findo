package httpapi

import (
	"log"

	"github.com/thoff/findo/backend/internal/index"
	"github.com/thoff/findo/backend/internal/smbclient"
)

// Crawl walks the SMB share and reconciles the index against what it finds:
// every visited file is upserted, then anything not visited this run (a
// deletion or rename on the NAS since the last crawl) is swept away. This
// still walks the whole tree each run — the incremental part is the SQLite
// write pattern, not directory discovery.
func (s *Server) Crawl() error {
	runID, err := s.idx.StartCrawlRun()
	if err != nil {
		return err
	}

	var files []index.File
	walkErr := s.smb.Walk("", func(e smbclient.Entry) error {
		dir, ext := index.SplitPath(e.Path)
		files = append(files, index.File{
			Path:    e.Path,
			Name:    e.Name,
			Dir:     dir,
			Ext:     ext,
			IsDir:   e.IsDir,
			Size:    e.Size,
			ModTime: e.ModTime,
		})
		return nil
	})

	if walkErr == nil {
		walkErr = s.idx.Reconcile(files, runID)
	}

	if err := s.idx.FinishCrawlRun(runID, walkErr); err != nil {
		log.Printf("record crawl run finish: %v", err)
	}

	if walkErr != nil {
		return walkErr
	}
	log.Printf("crawl complete: %d entries indexed", len(files))
	return nil
}
