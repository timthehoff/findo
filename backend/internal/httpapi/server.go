// Package httpapi wires the SMB client and index together behind an HTTP
// API: directory listing, search, Range-aware file streaming, health/stats,
// and a manual reindex trigger.
package httpapi

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/hirochachacha/go-smb2"

	"github.com/thoff/findo/backend/internal/dashboard"
	"github.com/thoff/findo/backend/internal/index"
	"github.com/thoff/findo/backend/internal/smbclient"
)

// smbClient is the subset of *smbclient.Client's behavior Server depends
// on, narrowed to an interface so tests can substitute a fake NAS instead
// of a live SMB session. *smbclient.Client satisfies this as-is.
type smbClient interface {
	Walk(root string, fn func(smbclient.Entry)) error
	Open(path string) (*smb2.File, os.FileInfo, error)
	Ping() error
	Stat(path string) (smbclient.Entry, bool, error)
	Watch(ctx context.Context, filter uint32) (<-chan smbclient.ChangeEvent, <-chan error)
}

type Server struct {
	smb smbClient
	idx *index.Index

	crawling atomic.Bool
}

func NewServer(smb smbClient, idx *index.Index) *Server {
	return &Server{smb: smb, idx: idx}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("GET /stats", s.handleStats)
	mux.HandleFunc("GET /files", s.handleList)
	mux.HandleFunc("GET /search", s.handleSearch)
	mux.HandleFunc("GET /files/content", s.handleContent)
	mux.HandleFunc("POST /reindex", s.handleReindex)
	mux.Handle("GET /", dashboard.Handler())
	return logMiddleware(mux)
}

func logMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.RequestURI(), time.Since(start))
	})
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("write json response: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	resp := map[string]interface{}{"status": "ok"}
	if err := s.smb.Ping(); err != nil {
		resp["status"] = "degraded"
		resp["smbError"] = err.Error()
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.idx.Stats()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	stats2 := struct {
		index.Stats
		Crawling bool `json:"crawling"`
	}{Stats: stats, Crawling: s.crawling.Load()}
	writeJSON(w, http.StatusOK, stats2)
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	dir := r.URL.Query().Get("path")
	files, err := s.idx.List(dir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"path": dir, "entries": files})
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if q == "" {
		writeError(w, http.StatusBadRequest, "missing required query param 'q'")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	files, err := s.idx.Search(q, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"query": q, "entries": files})
}

func (s *Server) handleContent(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("path")
	if p == "" {
		writeError(w, http.StatusBadRequest, "missing required query param 'path'")
		return
	}

	f, info, err := s.smb.Open(p)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	defer f.Close()

	http.ServeContent(w, r, info.Name(), info.ModTime(), f)
}

func (s *Server) handleReindex(w http.ResponseWriter, r *http.Request) {
	if !s.crawling.CompareAndSwap(false, true) {
		writeError(w, http.StatusConflict, "a crawl is already in progress")
		return
	}
	go func() {
		defer s.crawling.Store(false)
		if err := s.Crawl(); err != nil {
			log.Printf("crawl failed: %v", err)
		}
	}()
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "crawl started"})
}
