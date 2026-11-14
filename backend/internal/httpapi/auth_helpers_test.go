package httpapi

import (
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/thoff/findo/backend/internal/auth"
	"github.com/thoff/findo/backend/internal/index"
)

// Most tests are about files, search and volumes, not sign-in, so each test
// server gets a default signed-in administrator and authRoutes attaches its
// session to requests that don't carry one. Tests of sign-in and isolation use
// srv.Routes() directly with explicit cookies.
var defaultUsers sync.Map // *Server -> *http.Cookie

// signedInUser creates a user and a session for them, returning the user and
// the cookie to send.
func signedInUser(t testing.TB, srv *Server, name, role string) (index.User, *http.Cookie) {
	t.Helper()
	u, err := srv.idx.CreateUser(name, role)
	if err != nil {
		t.Fatalf("CreateUser(%s): %v", name, err)
	}
	return u, sessionFor(t, srv, u)
}

func sessionFor(t testing.TB, srv *Server, u index.User) *http.Cookie {
	t.Helper()
	token, hash := auth.NewToken()
	if _, err := srv.idx.CreateSession(u.ID, nil, hash, time.Hour, "test", "192.0.2.1"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	return &http.Cookie{Name: sessionCookie, Value: token}
}

// useDefaultUser makes u the user authRoutes acts as for srv.
func useDefaultUser(srv *Server, c *http.Cookie) { defaultUsers.Store(srv, c) }

// newDefaultAdmin creates the test server's default administrator.
func newDefaultAdmin(t testing.TB, srv *Server) index.User {
	t.Helper()
	u, c := signedInUser(t, srv, "tester", index.RoleAdmin)
	useDefaultUser(srv, c)
	return u
}

func authRoutes(srv *Server) http.Handler {
	inner := srv.Routes()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := r.Cookie(sessionCookie); err != nil {
			if c, ok := defaultUsers.Load(srv); ok {
				r.AddCookie(c.(*http.Cookie))
			}
		}
		inner.ServeHTTP(w, r)
	})
}
