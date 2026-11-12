package httpapi

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/thoff/findo/backend/internal/index"
)

// volumeInput is the request body for creating/updating a volume. Password
// is write-only: it's never echoed back, and on update an empty value
// leaves the stored password unchanged.
type volumeInput struct {
	Name     string `json:"name"`
	Host     string `json:"host"`
	Share    string `json:"share"`
	Username string `json:"username"`
	Password string `json:"password,omitempty"`
	Enabled  *bool  `json:"enabled,omitempty"`
	// ContentIndex opts the volume in to (or out of) file-content indexing;
	// omitted leaves it as it is.
	ContentIndex *bool `json:"contentIndex,omitempty"`
}

// volumeStatus is a Volume enriched with what's only known at runtime:
// live connection/crawl state, change-notify health, and index size, not
// just configuration.
type volumeStatus struct {
	index.Volume
	Crawling       bool   `json:"crawling"`
	Connected      bool   `json:"connected"`
	WatchConnected bool   `json:"watchConnected"`
	LastEventAt    string `json:"lastEventAt,omitempty"`
	ResyncCount    int    `json:"resyncCount"`
	FileCount      int    `json:"fileCount"`
	DirCount       int    `json:"dirCount"`

	LastCrawlStartedAt    string `json:"lastCrawlStartedAt,omitempty"`
	LastCrawlFinishedAt   string `json:"lastCrawlFinishedAt,omitempty"`
	LastCrawlTrigger      string `json:"lastCrawlTrigger,omitempty"`
	LastCrawlDurationMS   int64  `json:"lastCrawlDurationMs,omitempty"`
	LastCrawlFilesSeen    int    `json:"lastCrawlFilesSeen,omitempty"`
	LastCrawlFilesRemoved int64  `json:"lastCrawlFilesRemoved,omitempty"`
	LastCrawlBytesIndexed int64  `json:"lastCrawlBytesIndexed,omitempty"`
	LastCrawlError        string `json:"lastCrawlError,omitempty"`
}

func (s *Server) enrichVolume(v index.Volume) volumeStatus {
	vs := volumeStatus{Volume: v}

	if stats, err := s.idx.VolumeStats(v.ID); err == nil {
		vs.FileCount, vs.DirCount = stats.FileCount, stats.DirCount
	}
	if cr, ok, err := s.idx.LatestCrawlRun(v.ID); err == nil && ok {
		vs.LastCrawlStartedAt = cr.StartedAt
		vs.LastCrawlFinishedAt = cr.FinishedAt
		vs.LastCrawlTrigger = cr.Trigger
		vs.LastCrawlDurationMS = cr.DurationMS
		vs.LastCrawlFilesSeen = cr.FilesSeen
		vs.LastCrawlFilesRemoved = cr.FilesRemoved
		vs.LastCrawlBytesIndexed = cr.BytesIndexed
		vs.LastCrawlError = cr.Error
	}
	if rt := s.volumeRuntime(v.ID); rt != nil {
		vs.Connected = true
		vs.Crawling = rt.crawling.Load()
		connected, lastEventAt, resyncCount := rt.watchHealth()
		vs.WatchConnected = connected
		vs.ResyncCount = resyncCount
		if !lastEventAt.IsZero() {
			vs.LastEventAt = lastEventAt.UTC().Format(time.RFC3339)
		}
	}

	return vs
}

// otherVolume is all an administrator learns about someone else's volume: that
// it exists, what it's called and who owns it. Not where it points, and never
// its files.
type otherVolume struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	OwnerID   *int64 `json:"ownerId"`
	OwnerName string `json:"ownerName,omitempty"`
}

// handleListVolumes lists the caller's volumes in full. An administrator also
// gets "others": everyone else's volumes, by name and owner only.
func (s *Server) handleListVolumes(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	vols, err := s.idx.ListVolumesByOwner(p.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]volumeStatus, 0, len(vols))
	for _, v := range vols {
		out = append(out, s.enrichVolume(v))
	}
	resp := map[string]interface{}{"volumes": out}

	if p.IsAdmin() {
		others, err := s.otherVolumes(p)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		resp["others"] = others
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) otherVolumes(p Principal) ([]otherVolume, error) {
	all, err := s.idx.ListVolumes()
	if err != nil {
		return nil, err
	}
	users, err := s.idx.ListUsers()
	if err != nil {
		return nil, err
	}
	names := map[int64]string{}
	for _, u := range users {
		names[u.ID] = u.Username
	}
	others := []otherVolume{}
	for _, v := range all {
		if v.OwnerID != nil && *v.OwnerID == p.UserID {
			continue
		}
		o := otherVolume{ID: v.ID, Name: v.Name, OwnerID: v.OwnerID}
		if v.OwnerID != nil {
			o.OwnerName = names[*v.OwnerID]
		}
		others = append(others, o)
	}
	return others, nil
}

// writeVolumeWriteError reports a failed volume create/update: a name clash is
// the caller's to fix (409); anything else is a server error.
func writeVolumeWriteError(w http.ResponseWriter, err error) {
	if errors.Is(err, index.ErrVolumeNameTaken) {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeError(w, http.StatusInternalServerError, err.Error())
}

func (s *Server) handleCreateVolume(w http.ResponseWriter, r *http.Request) {
	var in volumeInput
	if !decodeJSON(w, r, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || in.Host == "" || in.Share == "" || in.Username == "" || in.Password == "" {
		writeError(w, http.StatusBadRequest, "name, host, share, username, and password are all required")
		return
	}

	owner := principalFrom(r).UserID
	vol, err := s.idx.CreateVolume(owner, in.Name, in.Host, in.Share, in.Username, func(volumeID int64) ([]byte, error) {
		return sealPassword(s.masterKey, volumeID, &owner, in.Password)
	})
	if err != nil {
		writeVolumeWriteError(w, err)
		return
	}
	if in.ContentIndex != nil && *in.ContentIndex {
		if vol, err = s.idx.SetVolumeContentIndex(vol.ID, true); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		s.kickContent()
	}

	if in.Enabled == nil || *in.Enabled {
		s.startVolumeAsync(vol)
	}

	writeJSON(w, http.StatusCreated, s.enrichVolume(vol))
}

func (s *Server) handleUpdateVolume(w http.ResponseWriter, r *http.Request) {
	existing := volumeFrom(r)
	id := existing.ID

	var in volumeInput
	if !decodeJSON(w, r, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || in.Host == "" || in.Share == "" || in.Username == "" {
		writeError(w, http.StatusBadRequest, "name, host, share, and username are required")
		return
	}

	// Pointing a volume at a different server while keeping its stored password
	// would make the server authenticate to that server with the NAS password.
	// So changing where it connects (or as whom) means typing the password again.
	if in.Password == "" && (in.Host != existing.Host || in.Share != existing.Share || in.Username != existing.Username) {
		writeError(w, http.StatusBadRequest, "changing the host, share or username requires entering the password again, so the stored password can't be sent to a different server")
		return
	}

	u := index.VolumeUpdate{
		Name: in.Name, Host: in.Host, Share: in.Share, Username: in.Username,
		Enabled: existing.Enabled,
	}
	if in.Enabled != nil {
		u.Enabled = *in.Enabled
	}
	if in.Password != "" {
		passwordEnc, err := sealPassword(s.masterKey, id, existing.OwnerID, in.Password)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "encrypt password: "+err.Error())
			return
		}
		u.PasswordEnc = passwordEnc
	}

	vol, err := s.idx.UpdateVolume(id, u)
	if err != nil {
		writeVolumeWriteError(w, err)
		return
	}
	if in.ContentIndex != nil {
		if vol, err = s.idx.SetVolumeContentIndex(id, *in.ContentIndex); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		s.kickContent()
	}

	// Connection details (or enabled state) may have changed — restart the
	// runtime unconditionally rather than trying to detect exactly what
	// changed; reconnecting on a pure rename is a cheap no-op to get wrong
	// safely.
	s.stopVolume(id)
	if vol.Enabled {
		s.startVolumeAsync(vol)
	}

	writeJSON(w, http.StatusOK, s.enrichVolume(vol))
}

func (s *Server) handleDeleteVolume(w http.ResponseWriter, r *http.Request) {
	id := volumeFrom(r).ID
	s.stopVolume(id)
	if err := s.idx.DeleteVolume(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleTestVolume re-tests a saved volume's stored credentials.
func (s *Server) handleTestVolume(w http.ResponseWriter, r *http.Request) {
	id := volumeFrom(r).ID

	secret, err := s.idx.GetVolumeSecret(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "volume not found")
		return
	}
	password, err := openPassword(s.masterKey, secret.ID, secret.OwnerID, secret.PasswordEnc)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "decrypt password: "+err.Error())
		return
	}

	testErr := s.testConnection(secret.Host, secret.Share, secret.Username, password)
	if err := s.idx.SetVolumeTestResult(id, testErr == nil, errMsg(testErr)); err != nil {
		log.Printf("record test result for volume %d: %v", id, err)
	}
	writeTestResult(w, testErr)
}

// handleTestVolumeInput tests connection details supplied directly in the
// request body, without persisting anything — used by the "add volume"
// form to validate credentials before saving them.
func (s *Server) handleTestVolumeInput(w http.ResponseWriter, r *http.Request) {
	var in volumeInput
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.Host == "" || in.Share == "" || in.Username == "" || in.Password == "" {
		writeError(w, http.StatusBadRequest, "host, share, username, and password are all required")
		return
	}

	testErr := s.testConnection(in.Host, in.Share, in.Username, in.Password)
	writeTestResult(w, testErr)
}

func (s *Server) testConnection(host, share, user, pass string) error {
	smb := s.newSMB(host, share, user, pass)
	defer smb.Close()
	if err := smb.Connect(); err != nil {
		return err
	}
	return smb.Ping()
}

func writeTestResult(w http.ResponseWriter, testErr error) {
	if testErr != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": testErr.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true})
}

// startVolumeAsync connects a newly created/updated volume in the
// background so the CRUD request that triggered it doesn't block on a slow
// or unreachable NAS. A failure is recorded as a test result so it's
// visible in the volume list rather than only in the server log.
func (s *Server) startVolumeAsync(vol index.Volume) {
	go func() {
		if err := s.StartVolume(context.Background(), vol); err != nil {
			log.Printf("volume %d (%s): failed to start: %v", vol.ID, vol.Name, err)
			if err := s.idx.SetVolumeTestResult(vol.ID, false, err.Error()); err != nil {
				log.Printf("record start failure for volume %d: %v", vol.ID, err)
			}
		}
	}()
}

func errMsg(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
