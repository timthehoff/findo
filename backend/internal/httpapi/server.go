// Package httpapi wires the SMB client and index together behind an HTTP
// API: volume configuration (with encrypted credentials), directory
// listing, search, Range-aware file streaming, health/stats, and manual
// crawl triggers. One Server can run several independently-crawled SMB
// volumes concurrently.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hirochachacha/go-smb2"

	"github.com/thoff/findo/backend/internal/dashboard"
	"github.com/thoff/findo/backend/internal/index"
	"github.com/thoff/findo/backend/internal/smbclient"
)

// smbClient is the subset of *smbclient.Client's behavior Server depends
// on, narrowed to an interface so tests can substitute a fake NAS instead
// of a live SMB session.
type smbClient interface {
	Walk(root string, fn func(smbclient.Entry)) error
	Open(path string) (*smb2.File, os.FileInfo, error)
	Ping() error
	Stat(path string) (smbclient.Entry, bool, error)
	Watch(ctx context.Context, filter uint32) (<-chan smbclient.ChangeEvent, <-chan error)
}

// smbConnector additionally covers session lifecycle, so a Server can dial
// and tear down a volume's connection without knowing the concrete type
// backing it. *smbclient.Client satisfies this as-is.
type smbConnector interface {
	smbClient
	Connect() error
	Close()
}

// volumeRuntime is the live state for one connected volume: its SMB
// session, whether a crawl is currently running for it, and its
// change-notify watch lifecycle.
type volumeRuntime struct {
	smb    smbConnector
	cancel context.CancelFunc

	crawling atomic.Bool

	wmu            sync.Mutex
	watchConnected bool
	lastEventAt    time.Time
	resyncCount    int
}

func (rt *volumeRuntime) setWatchConnected(connected bool) {
	rt.wmu.Lock()
	rt.watchConnected = connected
	rt.wmu.Unlock()
}

func (rt *volumeRuntime) recordWatchEvent() {
	rt.wmu.Lock()
	rt.lastEventAt = time.Now()
	rt.wmu.Unlock()
}

func (rt *volumeRuntime) recordResync() {
	rt.wmu.Lock()
	rt.resyncCount++
	rt.wmu.Unlock()
}

// watchHealth snapshots the change-notify listener's live state for the
// dashboard: whether it's currently connected, when it last saw an event,
// and how many times it's had to fall back to a full resync.
func (rt *volumeRuntime) watchHealth() (connected bool, lastEventAt time.Time, resyncCount int) {
	rt.wmu.Lock()
	defer rt.wmu.Unlock()
	return rt.watchConnected, rt.lastEventAt, rt.resyncCount
}

type Server struct {
	idx       *index.Index
	masterKey []byte

	// newSMB constructs the SMB client for a volume; overridden in tests to
	// inject a fake NAS instead of dialing a real one.
	newSMB func(host, share, user, pass string) smbConnector

	mu      sync.Mutex
	volumes map[int64]*volumeRuntime
}

func NewServer(idx *index.Index, masterKey []byte) *Server {
	return &Server{
		idx:       idx,
		masterKey: masterKey,
		newSMB: func(host, share, user, pass string) smbConnector {
			return smbclient.New(host, share, user, pass)
		},
		volumes: map[int64]*volumeRuntime{},
	}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("GET /stats", s.handleStats)
	mux.HandleFunc("GET /files", s.handleList)
	mux.HandleFunc("GET /search", s.handleSearch)
	mux.HandleFunc("GET /files/content", s.handleContent)
	mux.HandleFunc("GET /volumes", s.handleListVolumes)
	mux.HandleFunc("POST /volumes", s.handleCreateVolume)
	mux.HandleFunc("PUT /volumes/{id}", s.handleUpdateVolume)
	mux.HandleFunc("DELETE /volumes/{id}", s.handleDeleteVolume)
	mux.HandleFunc("POST /volumes/test", s.handleTestVolumeInput)
	mux.HandleFunc("POST /volumes/{id}/test", s.handleTestVolume)
	mux.HandleFunc("POST /volumes/{id}/reindex", s.handleReindex)
	mux.HandleFunc("GET /volumes/{id}/crawl-runs", s.handleCrawlRuns)
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

func parseVolumeID(r *http.Request) (int64, error) {
	return strconv.ParseInt(r.PathValue("id"), 10, 64)
}

// parseVolumeIDParam reads the required "volume" query param used by
// endpoints that operate on one specific volume's files (list, content).
func parseVolumeIDParam(r *http.Request) (int64, error) {
	raw := r.URL.Query().Get("volume")
	if raw == "" {
		return 0, fmt.Errorf("missing required query param 'volume'")
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid 'volume' query param: %w", err)
	}
	return id, nil
}

// volumeRuntime looks up a connected volume's live state, or nil if it
// isn't currently connected (not yet started, disabled, or its connection
// attempt failed).
func (s *Server) volumeRuntime(id int64) *volumeRuntime {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.volumes[id]
}

// registerVolumeRuntime records rt as volumeID's live state, replacing and
// tearing down any runtime already registered for it.
func (s *Server) registerVolumeRuntime(volumeID int64, smb smbConnector, cancel context.CancelFunc) *volumeRuntime {
	rt := &volumeRuntime{smb: smb, cancel: cancel}

	s.mu.Lock()
	old, hadOld := s.volumes[volumeID]
	s.volumes[volumeID] = rt
	s.mu.Unlock()

	if hadOld {
		old.cancel()
		old.smb.Close()
	}
	return rt
}

// stopVolume tears down volumeID's runtime, if any: stops its watch/
// periodic-crawl goroutines and closes its SMB session. It leaves the
// volume's configuration and indexed files untouched.
func (s *Server) stopVolume(volumeID int64) {
	s.mu.Lock()
	rt, ok := s.volumes[volumeID]
	if ok {
		delete(s.volumes, volumeID)
	}
	s.mu.Unlock()

	if !ok {
		return
	}
	rt.cancel()
	rt.smb.Close()
}

// StartVolume connects to vol's SMB share, registers its runtime, and
// kicks off an initial crawl followed by its change-notify watch loop plus
// a periodic-crawl safety net. ctx bounds the volume's background
// goroutines; cancel it or call stopVolume to tear it down.
func (s *Server) StartVolume(ctx context.Context, vol index.Volume) error {
	secret, err := s.idx.GetVolumeSecret(vol.ID)
	if err != nil {
		return fmt.Errorf("load volume %d secret: %w", vol.ID, err)
	}
	password, err := decryptPassword(s.masterKey, secret.PasswordEnc)
	if err != nil {
		return fmt.Errorf("decrypt volume %d password: %w", vol.ID, err)
	}

	smb := s.newSMB(secret.Host, secret.Share, secret.Username, password)
	if err := smb.Connect(); err != nil {
		return fmt.Errorf("connect volume %d: %w", vol.ID, err)
	}

	volCtx, cancel := context.WithCancel(ctx)
	rt := s.registerVolumeRuntime(vol.ID, smb, cancel)

	go func() {
		rt.crawling.Store(true)
		log.Printf("volume %d (%s): running initial crawl...", vol.ID, vol.Name)
		if err := s.Crawl(vol.ID, triggerStartup); err != nil {
			log.Printf("volume %d (%s): initial crawl failed: %v", vol.ID, vol.Name, err)
		}
		rt.crawling.Store(false)

		log.Printf("volume %d (%s): starting change-notify listener...", vol.ID, vol.Name)
		s.Watch(volCtx, vol.ID)
	}()
	go s.PeriodicCrawl(volCtx, vol.ID, periodicCrawlInterval)

	return nil
}

var errCrawlInProgress = errors.New("a crawl is already in progress for this volume")

// triggerCrawl starts a background crawl for volumeID unless one is
// already running for it, recording trigger as why it started.
func (s *Server) triggerCrawl(volumeID int64, trigger string) error {
	rt := s.volumeRuntime(volumeID)
	if rt == nil {
		return fmt.Errorf("volume %d is not connected", volumeID)
	}
	if !rt.crawling.CompareAndSwap(false, true) {
		return errCrawlInProgress
	}
	go func() {
		defer rt.crawling.Store(false)
		if err := s.Crawl(volumeID, trigger); err != nil {
			log.Printf("volume %d: crawl failed: %v", volumeID, err)
		}
	}()
	return nil
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	vols, err := s.idx.ListVolumes()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	type volumeHealth struct {
		ID    int64  `json:"id"`
		Name  string `json:"name"`
		OK    bool   `json:"ok"`
		Error string `json:"error,omitempty"`
	}

	status := "ok"
	results := make([]volumeHealth, 0, len(vols))
	for _, v := range vols {
		if !v.Enabled {
			continue
		}
		vh := volumeHealth{ID: v.ID, Name: v.Name}
		if rt := s.volumeRuntime(v.ID); rt == nil {
			vh.Error = "not connected"
		} else if err := rt.smb.Ping(); err != nil {
			vh.Error = err.Error()
		} else {
			vh.OK = true
		}
		if !vh.OK {
			status = "degraded"
		}
		results = append(results, vh)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"status": status, "volumes": results})
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.idx.Stats()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	volumeID, err := parseVolumeIDParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	dir := r.URL.Query().Get("path")
	files, err := s.idx.List(volumeID, dir)
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

	var volumeID int64
	if raw := r.URL.Query().Get("volume"); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid 'volume' query param")
			return
		}
		volumeID = id
	}

	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	files, err := s.idx.Search(volumeID, q, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"query": q, "entries": files})
}

func (s *Server) handleContent(w http.ResponseWriter, r *http.Request) {
	volumeID, err := parseVolumeIDParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	p := r.URL.Query().Get("path")
	if p == "" {
		writeError(w, http.StatusBadRequest, "missing required query param 'path'")
		return
	}

	rt := s.volumeRuntime(volumeID)
	if rt == nil {
		writeError(w, http.StatusNotFound, "volume is not connected")
		return
	}

	f, info, err := rt.smb.Open(p)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	defer f.Close()

	http.ServeContent(w, r, info.Name(), info.ModTime(), f)
}

func (s *Server) handleReindex(w http.ResponseWriter, r *http.Request) {
	id, err := parseVolumeID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid volume id")
		return
	}
	if err := s.triggerCrawl(id, triggerManual); err != nil {
		if errors.Is(err, errCrawlInProgress) {
			writeError(w, http.StatusConflict, err.Error())
		} else {
			writeError(w, http.StatusNotFound, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "crawl started"})
}

func (s *Server) handleCrawlRuns(w http.ResponseWriter, r *http.Request) {
	id, err := parseVolumeID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid volume id")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	runs, err := s.idx.CrawlRuns(id, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"runs": runs})
}
