package httpapi

import (
	"log"

	"github.com/thoff/findo/backend/internal/index"
	"github.com/thoff/findo/backend/internal/smbclient"
)

// Crawl walks the SMB share and replaces the entire index with what it
// finds. Simple full-replace strategy: correct and cheap enough at
// home-NAS scale, avoids reconciling adds/deletes/renames for milestone 1.
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
		walkErr = s.idx.ReplaceAll(files)
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
