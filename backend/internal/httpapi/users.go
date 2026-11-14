package httpapi

import (
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/thoff/findo/backend/internal/auth"
	"github.com/thoff/findo/backend/internal/index"
)

// Admin endpoints. Administrators manage accounts and hand volumes to other
// owners; none of it gives them a way to read another user's files.

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.idx.ListUsers()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"users": users})
}

type userInput struct {
	Username string  `json:"username"`
	Role     *string `json:"role"`
	Disabled *bool   `json:"disabled"`
}

// enrollmentBody is what issuing a link returns: the link itself, shown once.
func enrollmentBody(link string, expires time.Time) map[string]interface{} {
	return map[string]interface{}{"enrollmentLink": link, "expiresAt": expires.UTC().Format(time.RFC3339)}
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var in userInput
	if !decodeJSON(w, r, &in) {
		return
	}
	role := index.RoleUser
	if in.Role != nil {
		role = *in.Role
	}
	in.Username = strings.TrimSpace(in.Username)
	if !index.ValidUsername(in.Username) {
		writeError(w, http.StatusBadRequest, "username must be 1 to 64 letters, digits, dots, underscores or hyphens")
		return
	}
	if role != index.RoleAdmin && role != index.RoleUser {
		writeError(w, http.StatusBadRequest, "role must be admin or user")
		return
	}
	user, err := s.idx.CreateUser(in.Username, role)
	if errors.Is(err, index.ErrUsernameTaken) {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	by := principalFrom(r)
	link, expires, err := auth.IssueEnrollment(s.idx, s.publicURL, user.ID, index.EnrollFirst, &by.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "created the user but could not make an enrollment link: "+err.Error())
		return
	}
	log.Printf("auth: user %s created by %s", user, by.Username)
	body := enrollmentBody(link, expires)
	body["user"] = user
	writeJSON(w, http.StatusCreated, body)
}

func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(r)
	if !ok {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	var in userInput
	if !decodeJSON(w, r, &in) {
		return
	}
	by := principalFrom(r)
	// Changing your own role or disabling yourself is how an administrator
	// locks everyone out of administration by accident.
	if id == by.UserID && ((in.Role != nil && *in.Role != by.Role) || (in.Disabled != nil && *in.Disabled)) {
		writeError(w, http.StatusBadRequest, "you can't change your own role or disable your own account")
		return
	}
	user, err := s.idx.UpdateUser(id, in.Role, in.Disabled)
	switch {
	case errors.Is(err, index.ErrUserNotFound):
		writeError(w, http.StatusNotFound, "user not found")
	case errors.Is(err, index.ErrInvalidUser):
		writeError(w, http.StatusBadRequest, "role must be admin or user")
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
	default:
		log.Printf("auth: user %s updated by %s (role=%s disabled=%v)", user, by.Username, user.Role, user.Disabled)
		writeJSON(w, http.StatusOK, user)
	}
}

// handleDeleteUser removes a user and everything of theirs: their volumes
// (stopped, with their indexed files), passkeys and sessions.
func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(r)
	if !ok {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	by := principalFrom(r)
	if id == by.UserID {
		writeError(w, http.StatusBadRequest, "you can't delete your own account")
		return
	}
	user, found, err := s.idx.GetUser(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	vols, err := s.idx.ListVolumesByOwner(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, v := range vols {
		s.stopVolume(v.ID)
		if err := s.idx.DeleteVolume(v.ID); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	if err := s.idx.DeleteUser(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	log.Printf("auth: user %s deleted by %s (with %d volumes)", user, by.Username, len(vols))
	w.WriteHeader(http.StatusNoContent)
}

// handleUserEnrollment issues a fresh link for a user: their first, or a
// recovery after losing every device. It replaces any unused earlier link.
func (s *Server) handleUserEnrollment(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(r)
	if !ok {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	user, found, err := s.idx.GetUser(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	if user.Disabled {
		writeError(w, http.StatusConflict, "this user is disabled")
		return
	}
	creds, err := s.idx.ListCredentials(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	purpose := index.EnrollFirst
	if len(creds) > 0 {
		purpose = index.EnrollRecovery
	}
	by := principalFrom(r)
	link, expires, err := auth.IssueEnrollment(s.idx, s.publicURL, id, purpose, &by.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	log.Printf("auth: %s enrollment link issued for %s by %s", purpose, user, by.Username)
	writeJSON(w, http.StatusOK, enrollmentBody(link, expires))
}

// handleSetVolumeOwner hands a volume to another user. Their SMB password is
// bound to the owner, so it's resealed; nothing is exposed in the process.
func (s *Server) handleSetVolumeOwner(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(r)
	if !ok {
		writeError(w, http.StatusNotFound, "volume not found")
		return
	}
	var in struct {
		OwnerID int64 `json:"ownerId"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	vol, found, err := s.idx.GetVolume(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "volume not found")
		return
	}
	owner, found, err := s.idx.GetUser(in.OwnerID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !found {
		writeError(w, http.StatusBadRequest, "that user doesn't exist")
		return
	}
	updated, err := s.idx.SetVolumeOwner(id, owner.ID, func(enc []byte) ([]byte, error) {
		pw, err := openPassword(s.masterKey, id, vol.OwnerID, enc)
		if err != nil {
			return nil, err
		}
		return sealPassword(s.masterKey, id, &owner.ID, pw)
	})
	if err != nil {
		writeVolumeWriteError(w, err)
		return
	}
	log.Printf("auth: volume %d (%s) reassigned to %s by %s", id, vol.Name, owner, principalFrom(r).Username)
	writeJSON(w, http.StatusOK, updated)
}
