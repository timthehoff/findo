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

	"github.com/thoff/findo/backend/internal/dashboard"
	"github.com/thoff/findo/backend/internal/datequery"
	"github.com/thoff/findo/backend/internal/index"
	"github.com/thoff/findo/backend/internal/smbclient"
)

// smbClient is the subset of *smbclient.Client's behavior Server depends
// on, narrowed to an interface so tests can substitute a fake NAS instead
// of a live SMB session.
type smbClient interface {
	Walk(root string, fn func(smbclient.Entry)) error
	Open(path string) (smbclient.File, os.FileInfo, error)
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
	version   string // build version; "dev" for unreleased builds
	updates   *updateChecker

	// newSMB constructs the SMB client for a volume; overridden in tests to
	// inject a fake NAS instead of dialing a real one.
	newSMB func(host, share, user, pass string) smbConnector

	mu      sync.Mutex
	volumes map[int64]*volumeRuntime

	// Content indexer: contentKick wakes it early; the durations are fields
	// so tests can shorten them.
	contentKick    chan struct{}
	contentTimeout time.Duration
	contentIdle    time.Duration
}

func NewServer(idx *index.Index, masterKey []byte, version string) *Server {
	// 0.x releases are all published as pre-releases, so a 0.x install has to
	// look at them to ever hear about an update.
	cur, ok := parseSemver(version)
	includePre := ok && cur[0] == 0

	return &Server{
		idx:       idx,
		masterKey: masterKey,
		version:   version,
		updates:   newUpdateChecker(includePre),
		newSMB: func(host, share, user, pass string) smbConnector {
			return smbclient.New(host, share, user, pass)
		},
		volumes: map[int64]*volumeRuntime{},

		contentKick:    make(chan struct{}, 1),
		contentTimeout: defaultContentTimeout,
		contentIdle:    defaultContentIdle,
	}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("GET /version", s.handleVersion)
	mux.HandleFunc("GET /stats", s.handleStats)
	mux.HandleFunc("GET /filetypes", s.handleFileTypes)
	mux.HandleFunc("GET /settings", s.handleGetSettings)
	mux.HandleFunc("PUT /settings", s.handlePutSettings)
	mux.HandleFunc("GET /content/status", s.handleContentStatus)
	mux.HandleFunc("POST /volumes/{id}/content/retry", s.handleContentRetry)
	mux.HandleFunc("GET /files", s.handleList)
	mux.HandleFunc("GET /search", s.handleSearch)
	mux.HandleFunc("GET /files/text", s.handleFileText)
	mux.HandleFunc("GET /files/content", s.handleContent)
	// Same handler with the file's name as a cosmetic URL suffix: browsers
	// title a tab after the URL's last segment (images, text, media), so
	// links end in the real filename instead of "content". The name is
	// ignored; volume and path still come from the query string.
	mux.HandleFunc("GET /files/content/{name...}", s.handleContent)
	mux.HandleFunc("GET /volumes", s.handleListVolumes)
	mux.HandleFunc("POST /volumes", s.handleCreateVolume)
	mux.HandleFunc("PUT /volumes/{id}", s.handleUpdateVolume)
	mux.HandleFunc("DELETE /volumes/{id}", s.handleDeleteVolume)
	mux.HandleFunc("POST /volumes/test", s.handleTestVolumeInput)
	mux.HandleFunc("POST /volumes/{id}/test", s.handleTestVolume)
	mux.HandleFunc("POST /volumes/{id}/reindex", s.handleReindex)
	mux.HandleFunc("GET /volumes/{id}/crawl-runs", s.handleCrawlRuns)
	mux.HandleFunc("GET /volumes/{id}/insights", s.handleVolumeInsights)
	// The search page used to live at /search.html; keep old bookmarks and
	// home-screen pins working now that it's the landing page.
	mux.Handle("GET /search.html", http.RedirectHandler("/", http.StatusMovedPermanently))
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

// wantsHidden reports whether the request opted in to dotfiles (?hidden=1);
// they're left out of listings and search by default.
func wantsHidden(r *http.Request) bool {
	v := r.URL.Query().Get("hidden")
	return v == "1" || v == "true"
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	volumeID, err := parseVolumeIDParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	dir := r.URL.Query().Get("path")
	files, err := s.idx.List(volumeID, dir, wantsHidden(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"path": dir, "entries": files})
}

// handleSearch matches names, then the extracted text of files in volumes
// with content search on, filtered by modification time when given. The time
// range comes from a phrase in q ("report last month", "in March 2024") and/or
// explicit modified_after / modified_before params (YYYY-MM-DD or unix
// seconds), which override the phrase on their side. tz (an IANA name, sent
// by the browser) decides where days and months begin; the server's zone
// otherwise, which is UTC in a container.
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	params := r.URL.Query()
	q := params.Get("q")

	loc := time.Local
	if name := params.Get("tz"); name != "" {
		l, err := time.LoadLocation(name)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid 'tz' query param")
			return
		}
		loc = l
	}

	nameQuery, parsed, phrase := datequery.Parse(q, time.Now().In(loc))
	mod := parsed
	for _, p := range []struct {
		key string
		dst *int64
	}{{"modified_after", &mod.From}, {"modified_before", &mod.To}} {
		if raw := params.Get(p.key); raw != "" {
			ts, err := parseTimeParam(raw, loc)
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid '"+p.key+"' query param")
				return
			}
			*p.dst = ts
		}
	}
	if q == "" && mod.IsZero() {
		writeError(w, http.StatusBadRequest, "missing required query param 'q'")
		return
	}

	var volumeID int64
	if raw := params.Get("volume"); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid 'volume' query param")
			return
		}
		volumeID = id
	}

	limit, _ := strconv.Atoi(params.Get("limit"))
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	hidden := wantsHidden(r)
	files, err := s.idx.Search(volumeID, nameQuery, limit, hidden, index.TimeRange(mod))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Name matches come first and keep their place; whatever room is left
	// goes to files whose extracted text matches.
	var entries []searchEntry
	for _, f := range files {
		entries = append(entries, searchEntry{File: f, Match: "name"})
	}
	contentMatches := 0
	if nameQuery != "" && len(files) < limit {
		hits, err := s.idx.SearchContent(volumeID, nameQuery, limit-len(files), hidden, index.TimeRange(mod))
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		for _, h := range hits {
			chunk := h.Chunk
			entries = append(entries, searchEntry{File: h.File, Match: "content", Snippet: h.Snippet, Chunk: &chunk})
		}
		contentMatches = len(hits)
	}

	resp := map[string]interface{}{"query": q, "nameQuery": nameQuery, "entries": entries, "contentMatches": contentMatches}
	if !mod.IsZero() {
		resp["modified"] = map[string]interface{}{"from": mod.From, "to": mod.To, "phrase": phrase}
	}
	writeJSON(w, http.StatusOK, resp)
}

// searchEntry is one /search result: a file whose name matched, or one whose
// extracted text matched, with the best passage (index.MarkStart/MarkEnd
// surround the matched words; clients escape it before inserting the marks).
type searchEntry struct {
	index.File
	Match   string `json:"match"` // "name" or "content"
	Snippet string `json:"snippet,omitempty"`
	// Chunk is which chunk of the file's extracted text the snippet came from
	// (a pointer so chunk 0 isn't dropped); see /files/text.
	Chunk *int `json:"chunk,omitempty"`
}

// parseTimeParam accepts YYYY-MM-DD (start of that day in loc) or unix seconds.
func parseTimeParam(raw string, loc *time.Location) (int64, error) {
	if t, err := time.ParseInLocation("2006-01-02", raw, loc); err == nil {
		return t.Unix(), nil
	}
	return strconv.ParseInt(raw, 10, 64)
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

	setContentHeaders(w.Header(), info.Name(), r.URL.Query().Get("download") == "1")
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

// handleVolumeInsights serves the data behind the dashboard's small
// per-volume charts: storage broken down by file extension, and the
// largest individual files.
func (s *Server) handleVolumeInsights(w http.ResponseWriter, r *http.Request) {
	id, err := parseVolumeID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid volume id")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))

	extensions, err := s.idx.ExtensionBreakdown(id, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	largestFiles, err := s.idx.LargestFiles(id, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"extensions": extensions, "largestFiles": largestFiles})
}
