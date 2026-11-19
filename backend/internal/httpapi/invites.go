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

// Invite links let people make their own accounts without an administrator being
// there. An administrator makes a link with an expiry and a number of uses;
// whoever opens it (/join#<token>) chooses a username and registers a passkey,
// and gets an ordinary account. Administrators are still made only by other
// administrators or the CLI.

const (
	defaultInviteHours = 7 * 24
	maxInviteHours     = 30 * 24
	defaultInviteUses  = 1
	maxInviteUses      = 100
	maxInviteLabel     = 60
)

type inviteInput struct {
	Label          string `json:"label"`
	ExpiresInHours int    `json:"expiresInHours"`
	MaxUses        int    `json:"maxUses"`
}

func (s *Server) handleListInvites(w http.ResponseWriter, r *http.Request) {
	invites, err := s.idx.ListInvites()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"invites": invites})
}

// handleCreateInvite makes an invite and returns its link, which is shown once:
// only a hash of the token is kept.
func (s *Server) handleCreateInvite(w http.ResponseWriter, r *http.Request) {
	var in inviteInput
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.ExpiresInHours == 0 {
		in.ExpiresInHours = defaultInviteHours
	}
	if in.MaxUses == 0 {
		in.MaxUses = defaultInviteUses
	}
	label := strings.TrimSpace(in.Label)
	switch {
	case in.ExpiresInHours < 1 || in.ExpiresInHours > maxInviteHours:
		writeError(w, http.StatusBadRequest, "expiresInHours must be between 1 and 720 (30 days)")
		return
	case in.MaxUses < 1 || in.MaxUses > maxInviteUses:
		writeError(w, http.StatusBadRequest, "maxUses must be between 1 and 100")
		return
	case len([]rune(label)) > maxInviteLabel:
		writeError(w, http.StatusBadRequest, "label is too long")
		return
	}

	token, hash := auth.NewToken()
	by := principalFrom(r)
	invite, err := s.idx.CreateInvite(label, hash, by.UserID, time.Duration(in.ExpiresInHours)*time.Hour, in.MaxUses)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	u := *s.publicURL
	u.Path = "/join"
	u.Fragment = token
	log.Printf("auth: invite %d (%q) created by %s: %d use(s), %dh", invite.ID, invite.Label, by.Username, invite.MaxUses, in.ExpiresInHours)
	writeJSON(w, http.StatusCreated, map[string]interface{}{"invite": invite, "link": u.String()})
}

func (s *Server) handleRevokeInvite(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(r)
	if !ok {
		writeError(w, http.StatusNotFound, "invite not found")
		return
	}
	found, err := s.idx.RevokeInvite(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "invite not found")
		return
	}
	log.Printf("auth: invite %d revoked by %s", id, principalFrom(r).Username)
	w.WriteHeader(http.StatusNoContent)
}

// ---- joining ---------------------------------------------------------------

type joinBeginInput struct {
	Token    string `json:"token"`
	Username string `json:"username"`
	Label    string `json:"label"`
}

// handleJoinBegin starts registering a passkey for an account that doesn't exist
// yet. The account's id is reserved now (the passkey is bound to it) and the
// account itself is made only when the passkey checks out.
func (s *Server) handleJoinBegin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store") // challenges, and a session cookie
	if s.throttled(w, r) {
		return
	}
	var in joinBeginInput
	if !decodeJSON(w, r, &in) {
		return
	}
	invite, err := s.idx.LookupInvite(auth.HashToken(in.Token))
	if errors.Is(err, index.ErrInviteInvalid) {
		s.limiter.Fail(clientIP(r))
		log.Printf("auth: invite rejected ip=%s: invalid, used up or expired link", clientIP(r))
		writeError(w, http.StatusBadRequest, index.ErrInviteInvalid.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	username := strings.TrimSpace(in.Username)
	if !index.ValidUsername(username) {
		writeError(w, http.StatusBadRequest, "a name is 1 to 64 letters, digits, dots, underscores or hyphens, starting with a letter or digit")
		return
	}
	if _, taken, err := s.idx.GetUserByName(username); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	} else if taken {
		// Counted like a failed guess: otherwise anyone holding a link could test
		// which names exist, as often as they like.
		s.limiter.Fail(clientIP(r))
		writeError(w, http.StatusConflict, index.ErrUsernameTaken.Error())
		return
	}

	userID, err := s.idx.ReserveUserID()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not start registration")
		return
	}
	creation, session, err := s.wa.BeginRegistration(auth.WebAuthnUser{User: index.User{ID: userID, Username: username}}, auth.RegistrationOptions(nil)...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not start registration")
		return
	}
	id, ok := s.challenges.Put(clientIP(r), auth.Challenge{
		Kind: auth.KindJoin, Session: *session, InviteID: invite.ID, UserID: userID, Username: username, Label: cleanLabel(in.Label),
	})
	if !ok {
		writeBusy(w)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"challengeId": id, "options": creation})
}

func (s *Server) handleJoinFinish(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store") // challenges, and a session cookie
	if s.throttled(w, r) {
		return
	}
	ch, ok := s.takeChallenge(w, r, auth.KindJoin)
	if !ok {
		return
	}
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	cred, err := s.createCredential(index.User{ID: ch.UserID, Username: ch.Username}, nil, ch, body)
	if err != nil {
		s.limiter.Fail(clientIP(r))
		log.Printf("auth: join failed ip=%s: %v", clientIP(r), err)
		writeError(w, http.StatusBadRequest, "the passkey could not be verified")
		return
	}
	user, saved, err := s.idx.CompleteInvite(ch.InviteID, ch.UserID, ch.Username, auth.FromWebAuthn(ch.UserID, cred, ch.Label))
	switch {
	case errors.Is(err, index.ErrInviteInvalid):
		s.limiter.Fail(clientIP(r))
		writeError(w, http.StatusBadRequest, index.ErrInviteInvalid.Error())
		return
	case errors.Is(err, index.ErrUsernameTaken), errors.Is(err, index.ErrCredentialUsed):
		writeError(w, http.StatusConflict, err.Error())
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, "could not create the account")
		return
	}
	if !s.startSession(w, r, user, &saved.ID) {
		return
	}
	s.limiter.Succeed(clientIP(r))
	log.Printf("auth: invite %d used: new user %s ip=%s passkey=%d", ch.InviteID, user, clientIP(r), saved.ID)
	writeJSON(w, http.StatusCreated, map[string]interface{}{"user": userBody(user), "passkey": saved})
}
