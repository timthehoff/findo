package httpapi

import (
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/thoff/findo/backend/internal/auth"
	"github.com/thoff/findo/backend/internal/index"
)

// The WebAuthn ceremonies are two calls each. begin returns the options for
// the browser plus a challengeId; finish is POSTed the browser's response as the
// body, with ?challenge=<challengeId>. A challenge is single use and lasts five
// minutes (auth.Challenges).

const maxPasskeyLabel = 60

// SetPublicURL fixes the address passkeys are bound to. It must be called
// before the server takes requests; NewServer starts with http://localhost.
func (s *Server) SetPublicURL(u *url.URL) error {
	wa, err := auth.NewWebAuthn(u)
	if err != nil {
		return err
	}
	s.publicURL, s.wa = u, wa
	return nil
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// userBody is the signed-in user as the sign-in endpoints and /auth/me report it.
func userBody(u index.User) map[string]interface{} {
	return map[string]interface{}{"id": u.ID, "username": u.Username, "role": u.Role}
}

// throttled refuses a client that has failed too often lately, with 429.
func (s *Server) throttled(w http.ResponseWriter, r *http.Request) bool {
	ok, wait := s.limiter.Allow(clientIP(r))
	if ok {
		return false
	}
	secs := int(wait.Seconds()) + 1
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	writeError(w, http.StatusTooManyRequests, "too many failed attempts; try again in "+strconv.Itoa(secs)+" seconds")
	return true
}

// authFailed records a failed attempt and answers with one generic message, so
// the response never says whether a user, a passkey or a link exists.
func (s *Server) authFailed(w http.ResponseWriter, r *http.Request, what string, err error) {
	s.limiter.Fail(clientIP(r))
	log.Printf("auth: %s failed ip=%s: %v", what, clientIP(r), err)
	writeError(w, http.StatusUnauthorized, "sign-in failed")
}

// readBody reads a size-limited request body.
func readBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxJSONBody))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
		} else {
			writeError(w, http.StatusBadRequest, "could not read the request body")
		}
		return nil, false
	}
	return body, true
}

// takeChallenge claims the challenge named by ?challenge=, which must be of
// the given kind.
func (s *Server) takeChallenge(w http.ResponseWriter, r *http.Request, kind string) (auth.Challenge, bool) {
	ch, ok := s.challenges.Take(r.URL.Query().Get("challenge"))
	if !ok || ch.Kind != kind {
		writeError(w, http.StatusBadRequest, "this attempt expired or was already used; start again")
		return auth.Challenge{}, false
	}
	return ch, true
}

func cleanLabel(s string) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > maxPasskeyLabel {
		s = string(r[:maxPasskeyLabel])
	}
	if s == "" {
		return "Passkey"
	}
	return s
}

// ---- sign in ---------------------------------------------------------------

func (s *Server) handleLoginBegin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store") // challenges, and a session cookie
	if s.throttled(w, r) {
		return
	}
	assertion, session, err := s.wa.BeginDiscoverableLogin(auth.LoginOptions()...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not start sign-in")
		return
	}
	id, ok := s.challenges.Put(clientIP(r), auth.Challenge{Kind: auth.KindLogin, Session: *session})
	if !ok {
		writeBusy(w)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"challengeId": id, "options": assertion})
}

func (s *Server) handleLoginFinish(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store") // challenges, and a session cookie
	if s.throttled(w, r) {
		return
	}
	ch, ok := s.takeChallenge(w, r, auth.KindLogin)
	if !ok {
		return
	}
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(body)
	if err != nil {
		s.authFailed(w, r, "sign-in", err)
		return
	}

	var found auth.WebAuthnUser
	lookup := func(rawID, userHandle []byte) (webauthn.User, error) {
		uid, ok := auth.UserIDFromHandle(userHandle)
		if !ok {
			return nil, errors.New("unrecognized user handle")
		}
		u, ok, err := s.idx.GetUser(uid)
		if err != nil {
			return nil, err
		}
		if !ok || u.Disabled {
			return nil, errors.New("unknown or disabled user")
		}
		creds, err := s.idx.ListCredentials(uid)
		if err != nil {
			return nil, err
		}
		found = auth.WebAuthnUser{User: u, Credentials: creds}
		return found, nil
	}
	_, cred, err := s.wa.ValidatePasskeyLogin(lookup, ch.Session, parsed)
	if err != nil {
		s.authFailed(w, r, "sign-in", err)
		return
	}

	var stored *index.Credential
	for i := range found.Credentials {
		if string(found.Credentials[i].CredentialID) == string(cred.ID) {
			stored = &found.Credentials[i]
		}
	}
	if stored == nil { // can't happen: the library matched it against this list
		s.authFailed(w, r, "sign-in", errors.New("credential vanished"))
		return
	}
	if cred.Authenticator.CloneWarning {
		// The authenticator's counter didn't advance: two copies of this passkey
		// may exist. Findo logs it rather than refusing, since some synced
		// passkeys never count.
		log.Printf("auth: WARNING sign-in with passkey %d (%q) of %s reported a non-increasing sign count; the authenticator may be cloned", stored.ID, stored.Label, found.User)
	}
	if err := s.idx.RecordCredentialUse(stored.ID, cred.Authenticator.SignCount, cred.Flags.BackupState, true); err != nil {
		writeError(w, http.StatusInternalServerError, "could not complete sign-in")
		return
	}
	if !s.startSession(w, r, found.User, &stored.ID) {
		return
	}
	s.limiter.Succeed(clientIP(r))
	log.Printf("auth: sign-in ok user=%s ip=%s passkey=%d", found.User, clientIP(r), stored.ID)
	writeJSON(w, http.StatusOK, map[string]interface{}{"user": userBody(found.User)})
}

// startSession signs u in: a new server-side session and its cookie.
func (s *Server) startSession(w http.ResponseWriter, r *http.Request, u index.User, credentialRowID *int64) bool {
	token, hash := auth.NewToken()
	if _, err := s.idx.CreateSession(u.ID, credentialRowID, hash, index.SessionTTL, r.UserAgent(), clientIP(r)); err != nil {
		log.Printf("auth: create session for %s: %v", u, err)
		writeError(w, http.StatusInternalServerError, "could not complete sign-in")
		return false
	}
	s.setSessionCookie(w, r, token)
	return true
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"user": map[string]interface{}{"id": p.UserID, "username": p.Username, "role": p.Role},
	})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		if err := s.idx.DeleteSessionByToken(auth.HashToken(c.Value)); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	clearSessionCookie(w, r, s.requestIsHTTPS(r))
	w.WriteHeader(http.StatusNoContent)
}

// ---- enrollment ------------------------------------------------------------

type enrollBeginInput struct {
	Token string `json:"token"`
	Label string `json:"label"`
}

func (s *Server) handleEnrollBegin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store") // challenges, and a session cookie
	if s.throttled(w, r) {
		return
	}
	var in enrollBeginInput
	if !decodeJSON(w, r, &in) {
		return
	}
	e, err := s.idx.LookupEnrollment(auth.HashToken(in.Token))
	if errors.Is(err, index.ErrEnrollmentInvalid) {
		s.limiter.Fail(clientIP(r))
		log.Printf("auth: enrollment rejected ip=%s: invalid, used or expired link", clientIP(r))
		writeError(w, http.StatusBadRequest, index.ErrEnrollmentInvalid.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	creds, err := s.idx.ListCredentials(e.User.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	creation, session, err := s.wa.BeginRegistration(auth.WebAuthnUser{User: e.User, Credentials: creds}, auth.RegistrationOptions(creds)...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not start registration")
		return
	}
	id, ok := s.challenges.Put(clientIP(r), auth.Challenge{Kind: auth.KindEnroll, Session: *session, EnrollmentID: e.ID, UserID: e.User.ID, Label: cleanLabel(in.Label)})
	if !ok {
		writeBusy(w)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"challengeId": id, "options": creation, "username": e.User.Username})
}

func (s *Server) handleEnrollFinish(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store") // challenges, and a session cookie
	if s.throttled(w, r) {
		return
	}
	ch, ok := s.takeChallenge(w, r, auth.KindEnroll)
	if !ok {
		return
	}
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	user, ok, err := s.idx.GetUser(ch.UserID)
	if err != nil || !ok || user.Disabled {
		s.authFailed(w, r, "enrollment", errors.New("user gone or disabled"))
		return
	}
	creds, err := s.idx.ListCredentials(user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	cred, err := s.createCredential(user, creds, ch, body)
	if err != nil {
		s.authFailed(w, r, "enrollment", err)
		return
	}
	saved, err := s.idx.CompleteEnrollment(ch.EnrollmentID, auth.FromWebAuthn(user.ID, cred, ch.Label))
	switch {
	case errors.Is(err, index.ErrEnrollmentInvalid):
		s.authFailed(w, r, "enrollment", err)
		return
	case errors.Is(err, index.ErrCredentialUsed):
		writeError(w, http.StatusConflict, err.Error())
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, "could not save the passkey")
		return
	}
	if !s.startSession(w, r, user, &saved.ID) {
		return
	}
	s.limiter.Succeed(clientIP(r))
	log.Printf("auth: enrollment ok user=%s ip=%s passkey=%d", user, clientIP(r), saved.ID)
	writeJSON(w, http.StatusCreated, map[string]interface{}{"user": userBody(user), "passkey": saved})
}

// createCredential verifies a registration response against its challenge.
func (s *Server) createCredential(user index.User, creds []index.Credential, ch auth.Challenge, body []byte) (*webauthn.Credential, error) {
	parsed, err := protocol.ParseCredentialCreationResponseBytes(body)
	if err != nil {
		return nil, err
	}
	return s.wa.CreateCredential(auth.WebAuthnUser{User: user, Credentials: creds}, ch.Session, parsed)
}

// userKey is the owner a signed-in user's begun ceremonies are stored under.
func userKey(p Principal) string { return "user:" + strconv.FormatInt(p.UserID, 10) }

// writeBusy answers when the challenge store is full of other people's
// ceremonies.
func writeBusy(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "30")
	writeError(w, http.StatusServiceUnavailable, "too many sign-ins are in progress; try again in a moment")
}

// ---- confirming with a passkey -----------------------------------------------

// A session that has gone a few minutes without a passkey proving presence
// can't do the "fresh" routes (add or remove a passkey, issue sign-in links,
// manage users). It confirms with one of the user's own passkeys here, which
// is the same two-call ceremony as sign-in but bound to the signed-in user.

func (s *Server) handleReauthBegin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if s.throttled(w, r) {
		return
	}
	p := principalFrom(r)
	user, ok, err := s.idx.GetUser(p.UserID)
	if err != nil || !ok {
		writeError(w, http.StatusInternalServerError, "could not load your account")
		return
	}
	creds, err := s.idx.ListCredentials(p.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	assertion, session, err := s.wa.BeginLogin(auth.WebAuthnUser{User: user, Credentials: creds}, auth.LoginOptions()...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not start the confirmation")
		return
	}
	id, ok := s.challenges.Put(userKey(p), auth.Challenge{Kind: auth.KindReauth, Session: *session, UserID: p.UserID})
	if !ok {
		writeBusy(w)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"challengeId": id, "options": assertion})
}

func (s *Server) handleReauthFinish(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if s.throttled(w, r) {
		return
	}
	p := principalFrom(r)
	ch, ok := s.takeChallenge(w, r, auth.KindReauth)
	if !ok {
		return
	}
	if ch.UserID != p.UserID { // someone else's challenge id
		writeError(w, http.StatusBadRequest, "this attempt expired or was already used; start again")
		return
	}
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	fail := func(err error) {
		s.limiter.Fail(clientIP(r))
		log.Printf("auth: confirmation failed user=%s ip=%s: %v", p.Username, clientIP(r), err)
		writeError(w, http.StatusUnauthorized, "confirmation failed")
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(body)
	if err != nil {
		fail(err)
		return
	}
	user, ok, err := s.idx.GetUser(p.UserID)
	if err != nil || !ok {
		writeError(w, http.StatusInternalServerError, "could not load your account")
		return
	}
	creds, err := s.idx.ListCredentials(p.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	cred, err := s.wa.ValidateLogin(auth.WebAuthnUser{User: user, Credentials: creds}, ch.Session, parsed)
	if err != nil {
		fail(err)
		return
	}
	for i := range creds {
		if string(creds[i].CredentialID) != string(cred.ID) {
			continue
		}
		if cred.Authenticator.CloneWarning {
			log.Printf("auth: WARNING confirmation with passkey %d (%q) of %s reported a non-increasing sign count; the authenticator may be cloned", creds[i].ID, creds[i].Label, user)
		}
		if err := s.idx.RecordCredentialUse(creds[i].ID, cred.Authenticator.SignCount, cred.Flags.BackupState, false); err != nil {
			writeError(w, http.StatusInternalServerError, "could not record the confirmation")
			return
		}
	}
	if err := s.idx.MarkSessionVerified(p.SessionID, time.Now()); err != nil {
		writeError(w, http.StatusInternalServerError, "could not record the confirmation")
		return
	}
	s.limiter.Succeed(clientIP(r))
	log.Printf("auth: confirmation ok user=%s ip=%s", p.Username, clientIP(r))
	w.WriteHeader(http.StatusNoContent)
}

// ---- passkeys of the signed-in user ----------------------------------------

type labelInput struct {
	Label string `json:"label"`
}

func (s *Server) handleRegisterBegin(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	var in labelInput
	if r.ContentLength != 0 && !decodeJSON(w, r, &in) {
		return
	}
	user, ok, err := s.idx.GetUser(p.UserID)
	if err != nil || !ok {
		writeError(w, http.StatusInternalServerError, "could not load your account")
		return
	}
	creds, err := s.idx.ListCredentials(p.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	creation, session, err := s.wa.BeginRegistration(auth.WebAuthnUser{User: user, Credentials: creds}, auth.RegistrationOptions(creds)...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not start registration")
		return
	}
	id, ok := s.challenges.Put(userKey(p), auth.Challenge{Kind: auth.KindRegister, Session: *session, UserID: p.UserID, Label: cleanLabel(in.Label)})
	if !ok {
		writeBusy(w)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"challengeId": id, "options": creation})
}

func (s *Server) handleRegisterFinish(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	ch, ok := s.takeChallenge(w, r, auth.KindRegister)
	if !ok {
		return
	}
	if ch.UserID != p.UserID { // someone else's challenge id
		writeError(w, http.StatusBadRequest, "this attempt expired or was already used; start again")
		return
	}
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	user, ok, err := s.idx.GetUser(p.UserID)
	if err != nil || !ok {
		writeError(w, http.StatusInternalServerError, "could not load your account")
		return
	}
	creds, err := s.idx.ListCredentials(p.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	cred, err := s.createCredential(user, creds, ch, body)
	if err != nil {
		log.Printf("auth: passkey registration failed user=%s ip=%s: %v", user, clientIP(r), err)
		writeError(w, http.StatusBadRequest, "the passkey could not be verified")
		return
	}
	saved, err := s.idx.AddCredential(auth.FromWebAuthn(p.UserID, cred, ch.Label))
	if errors.Is(err, index.ErrCredentialUsed) {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not save the passkey")
		return
	}
	log.Printf("auth: passkey added user=%s ip=%s passkey=%d", user, clientIP(r), saved.ID)
	writeJSON(w, http.StatusCreated, map[string]interface{}{"passkey": saved})
}

func (s *Server) handleListPasskeys(w http.ResponseWriter, r *http.Request) {
	creds, err := s.idx.ListCredentials(principalFrom(r).UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"passkeys": creds})
}

func pathInt(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil
}

func (s *Server) handleRenamePasskey(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(r)
	var in labelInput
	if !ok || !decodeJSON(w, r, &in) {
		if !ok {
			writeError(w, http.StatusNotFound, "passkey not found")
		}
		return
	}
	if strings.TrimSpace(in.Label) == "" {
		writeError(w, http.StatusBadRequest, "label is required")
		return
	}
	found, err := s.idx.RenameCredential(principalFrom(r).UserID, id, cleanLabel(in.Label))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "passkey not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeletePasskey(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(r)
	if !ok {
		writeError(w, http.StatusNotFound, "passkey not found")
		return
	}
	p := principalFrom(r)
	found, err := s.idx.DeleteCredential(p.UserID, id)
	switch {
	case !found && err == nil:
		writeError(w, http.StatusNotFound, "passkey not found")
	case errors.Is(err, index.ErrLastCredential):
		writeError(w, http.StatusConflict, err.Error())
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
	default:
		log.Printf("auth: passkey removed user=%s ip=%s passkey=%d", p.Username, clientIP(r), id)
		w.WriteHeader(http.StatusNoContent)
	}
}

// ---- sessions of the signed-in user ----------------------------------------

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	sessions, err := s.idx.ListSessions(p.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	type row struct {
		index.Session
		Current bool `json:"current"`
	}
	out := make([]row, 0, len(sessions))
	for _, sess := range sessions {
		out = append(out, row{sess, sess.ID == p.SessionID})
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"sessions": out})
}

func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(r)
	if !ok {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	p := principalFrom(r)
	found, err := s.idx.DeleteSession(p.UserID, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	if id == p.SessionID {
		clearSessionCookie(w, r, s.requestIsHTTPS(r))
	}
	w.WriteHeader(http.StatusNoContent)
}
