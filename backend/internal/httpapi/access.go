package httpapi

import (
	"context"
	"net"
	"net/http"
	"strings"

	"github.com/thoff/findo/backend/internal/auth"
	"github.com/thoff/findo/backend/internal/dashboard"
	"github.com/thoff/findo/backend/internal/index"
)

// access is who may call a route. Every route is declared with one in
// routeSpecs, and guard enforces it before the handler runs, so a handler never
// has to remember to check, and a new route can't be added without choosing.
type access int

const (
	// accessPublic: anyone. Only sign-in, enrollment, the app's static files and
	// a bare liveness check.
	accessPublic access = iota
	// accessUser: any signed-in user. Handlers see only their own volumes, via
	// the Principal.
	accessUser
	// accessAdmin: a signed-in administrator.
	accessAdmin
	// accessVolumePath: signed in, and the {id} in the path must be a volume the
	// caller owns; anything else is a 404, so ids can't be probed.
	accessVolumePath
	// accessVolumeQuery: the same, for the "volume" query parameter.
	accessVolumeQuery
)

type routeSpec struct {
	pattern string
	access  access
	handler http.Handler
}

// Principal is the signed-in user a request acts as.
type Principal struct {
	UserID    int64
	Username  string
	Role      string
	SessionID int64
}

func (p Principal) IsAdmin() bool { return p.Role == index.RoleAdmin }

type ctxKey int

const (
	principalKey ctxKey = iota
	volumeKey
)

// principalFrom is the caller of a request that passed guard (any access but
// accessPublic).
func principalFrom(r *http.Request) Principal {
	p, _ := r.Context().Value(principalKey).(Principal)
	return p
}

// volumeFrom is the owned volume a accessVolumePath/accessVolumeQuery route
// was called for.
func volumeFrom(r *http.Request) index.Volume {
	v, _ := r.Context().Value(volumeKey).(index.Volume)
	return v
}

func (s *Server) routeSpecs() []routeSpec {
	h := func(pattern string, a access, fn http.HandlerFunc) routeSpec {
		return routeSpec{pattern, a, fn}
	}
	return []routeSpec{
		h("GET /healthz", accessPublic, s.handleHealthz),

		// Sign-in and enrollment: the only API open to anyone.
		h("POST /auth/passkey/login/begin", accessPublic, s.handleLoginBegin),
		h("POST /auth/passkey/login/finish", accessPublic, s.handleLoginFinish),
		h("POST /auth/enroll/begin", accessPublic, s.handleEnrollBegin),
		h("POST /auth/enroll/finish", accessPublic, s.handleEnrollFinish),

		h("GET /auth/me", accessUser, s.handleMe),
		h("POST /auth/logout", accessUser, s.handleLogout),
		h("POST /auth/passkey/register/begin", accessUser, s.handleRegisterBegin),
		h("POST /auth/passkey/register/finish", accessUser, s.handleRegisterFinish),
		h("GET /auth/passkeys", accessUser, s.handleListPasskeys),
		h("PUT /auth/passkeys/{id}", accessUser, s.handleRenamePasskey),
		h("DELETE /auth/passkeys/{id}", accessUser, s.handleDeletePasskey),
		h("GET /auth/sessions", accessUser, s.handleListSessions),
		h("DELETE /auth/sessions/{id}", accessUser, s.handleDeleteSession),

		h("GET /users", accessAdmin, s.handleListUsers),
		h("POST /users", accessAdmin, s.handleCreateUser),
		h("PUT /users/{id}", accessAdmin, s.handleUpdateUser),
		h("DELETE /users/{id}", accessAdmin, s.handleDeleteUser),
		h("POST /users/{id}/enrollment", accessAdmin, s.handleUserEnrollment),
		h("PUT /volumes/{id}/owner", accessAdmin, s.handleSetVolumeOwner),

		h("GET /health", accessUser, s.handleHealth),
		h("GET /version", accessUser, s.handleVersion),
		h("GET /stats", accessUser, s.handleStats),
		h("GET /filetypes", accessUser, s.handleFileTypes),
		h("GET /settings", accessAdmin, s.handleGetSettings),
		h("PUT /settings", accessAdmin, s.handlePutSettings),
		h("GET /content/status", accessUser, s.handleContentStatus),
		h("GET /search", accessUser, s.handleSearch),

		h("GET /files", accessVolumeQuery, s.handleList),
		h("GET /files/text", accessVolumeQuery, s.handleFileText),
		h("GET /files/content", accessVolumeQuery, s.handleContent),
		// Same handler with the file's name as a cosmetic URL suffix: browsers
		// title a tab after the URL's last segment (images, text, media), so
		// links end in the real filename instead of "content". The name is
		// ignored; volume and path still come from the query string.
		h("GET /files/content/{name...}", accessVolumeQuery, s.handleContent),

		h("GET /volumes", accessUser, s.handleListVolumes),
		h("POST /volumes", accessUser, s.handleCreateVolume),
		h("POST /volumes/test", accessUser, s.handleTestVolumeInput),
		h("PUT /volumes/{id}", accessVolumePath, s.handleUpdateVolume),
		h("DELETE /volumes/{id}", accessVolumePath, s.handleDeleteVolume),
		h("POST /volumes/{id}/test", accessVolumePath, s.handleTestVolume),
		h("POST /volumes/{id}/reindex", accessVolumePath, s.handleReindex),
		h("POST /volumes/{id}/content/retry", accessVolumePath, s.handleContentRetry),
		h("GET /volumes/{id}/crawl-runs", accessVolumePath, s.handleCrawlRuns),
		h("GET /volumes/{id}/insights", accessVolumePath, s.handleVolumeInsights),

		// The app's own HTML, CSS and JS carry no user data (every API call they
		// make is guarded above), so they're served to anyone, which is also what
		// lets the sign-in page load.
		//
		// The search page used to live at /search.html; keep old bookmarks and
		// home-screen pins working now that it's the landing page.
		{"GET /search.html", accessPublic, http.RedirectHandler("/", http.StatusMovedPermanently)},
		{"GET /", accessPublic, dashboard.Handler()},
	}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	for _, rt := range s.routeSpecs() {
		mux.Handle(rt.pattern, s.guard(rt.access, rt.handler))
	}
	return logMiddleware(mux)
}

// guard enforces a route's access level before calling next.
func (s *Server) guard(a access, next http.Handler) http.Handler {
	if a == accessPublic {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		su, token, ok, err := s.session(r)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "could not check your session")
			return
		}
		if !ok {
			s.writeUnauthorized(w)
			return
		}
		p := Principal{UserID: su.User.ID, Username: su.User.Username, Role: su.User.Role, SessionID: su.SessionID}
		if su.Refreshed {
			// The server-side expiry moved out; move the cookie's with it, or a
			// home-screen app would be signed out 30 days after sign-in however
			// often it was used.
			s.setSessionCookie(w, r, token)
		}
		if a == accessAdmin && !p.IsAdmin() {
			writeError(w, http.StatusForbidden, "administrator access required")
			return
		}

		ctx := context.WithValue(r.Context(), principalKey, p)
		if a == accessVolumePath || a == accessVolumeQuery {
			var id int64
			var err error
			if a == accessVolumePath {
				id, err = parseVolumeID(r)
				if err != nil {
					writeError(w, http.StatusBadRequest, "invalid volume id")
					return
				}
			} else if id, err = parseVolumeIDParam(r); err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			vol, ok, err := s.ownedVolume(p, id)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			if !ok {
				writeError(w, http.StatusNotFound, "volume not found")
				return
			}
			ctx = context.WithValue(ctx, volumeKey, vol)
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// ownedVolume loads volume id if p owns it. Someone else's volume, an unowned
// one and a missing one are indistinguishable to the caller.
func (s *Server) ownedVolume(p Principal, id int64) (index.Volume, bool, error) {
	vol, ok, err := s.idx.GetVolume(id)
	if err != nil || !ok || vol.OwnerID == nil || *vol.OwnerID != p.UserID {
		return index.Volume{}, false, err
	}
	return vol, true, nil
}

// scope is the volumes p may search and browse: their own. Every query that
// could span volumes takes this, never "all".
func (s *Server) scope(p Principal) ([]int64, error) {
	return s.idx.VolumeIDsByOwner(p.UserID)
}

const sessionCookie = "findo_session"

// session resolves the request's cookie to a live session.
func (s *Server) session(r *http.Request) (su index.SessionUser, token string, ok bool, err error) {
	c, cerr := r.Cookie(sessionCookie)
	if cerr != nil || c.Value == "" {
		return index.SessionUser{}, "", false, nil
	}
	su, ok, err = s.idx.SessionByToken(auth.HashToken(c.Value))
	return su, c.Value, ok, err
}

func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		MaxAge:   int(index.SessionTTL.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   s.requestIsHTTPS(r),
	})
}

func clearSessionCookie(w http.ResponseWriter, r *http.Request, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: secure,
	})
}

// requestIsHTTPS reports whether the browser reached Findo over HTTPS: directly,
// through a proxy on this machine that says so (X-Forwarded-Proto from anywhere
// else is a client's claim and ignored), or because the public URL is https.
func (s *Server) requestIsHTTPS(r *http.Request) bool {
	if r.TLS != nil || s.publicURL.Scheme == "https" {
		return true
	}
	return peerIsLoopback(r) && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func peerIsLoopback(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// clientIP is the address to rate-limit and log a request under. Behind a proxy
// on this machine (Caddy) every connection comes from loopback, so the proxy's
// X-Forwarded-For is used: its last entry is the one the proxy itself added.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if peerIsLoopback(r) {
		parts := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
		if last := strings.TrimSpace(parts[len(parts)-1]); net.ParseIP(last) != nil {
			return last
		}
	}
	return host
}

// writeUnauthorized answers a request with no valid session. Until the first
// account exists it says how to make one.
func (s *Server) writeUnauthorized(w http.ResponseWriter) {
	body := map[string]interface{}{"error": "sign in required"}
	if n, err := s.idx.CountUsers(); err == nil && n == 0 {
		body["setupRequired"] = true
		body["error"] = "no accounts exist yet: on the machine running Findo, run `findo-server user add --admin <name>` and open the link it prints"
	}
	writeJSON(w, http.StatusUnauthorized, body)
}
