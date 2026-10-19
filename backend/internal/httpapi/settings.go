package httpapi

import (
	"errors"
	"net/http"

	"github.com/thoff/findo/backend/internal/index"
)

// settingsBody is the shape of /settings: one object per feature area.
type settingsBody struct {
	Content *index.ContentSettings `json:"content"`
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	cs, err := s.idx.ContentSettings()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, settingsBody{Content: &cs})
}

// handlePutSettings merges the request over the saved settings, so a client
// may send just the fields it's changing (the pause button sends only
// "paused"). Out-of-range values are a 400 and change nothing.
func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	cur, err := s.idx.ContentSettings()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	body := settingsBody{Content: &cur}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Content == nil { // an explicit "content": null
		body.Content = &cur
	}

	if err := s.idx.SetContentSettings(*body.Content); err != nil {
		if errors.Is(err, index.ErrInvalidSettings) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.kickContent() // limits or pause changed: let the indexer notice now
	writeJSON(w, http.StatusOK, settingsBody{Content: body.Content})
}
