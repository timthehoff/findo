package httpapi

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/thoff/findo/backend/internal/auth"
	"github.com/thoff/findo/backend/internal/auth/authtest"
	"github.com/thoff/findo/backend/internal/index"
)

// The routes that need a recent passkey confirmation. Adding to or removing
// from this list changes what a stolen session can do, so it's pinned.
var wantFresh = []string{
	"POST /auth/passkey/register/begin",
	"DELETE /auth/passkeys/{id}",
	"POST /users",
	"PUT /users/{id}",
	"DELETE /users/{id}",
	"POST /users/{id}/enrollment",
	"PUT /volumes/{id}/owner",
}

func TestTheStepUpRoutesArePinned(t *testing.T) {
	srv := newBareServer(t)
	var got []string
	for _, rt := range srv.routeSpecs() {
		if rt.fresh {
			got = append(got, rt.pattern)
			if rt.access == accessPublic {
				t.Errorf("%s: a public route can't need a confirmation", rt.pattern)
			}
		}
	}
	sort.Strings(got)
	want := append([]string(nil), wantFresh...)
	sort.Strings(want)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("step-up routes are\n  %s\nwant exactly\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

// sessionIDOf finds the server-side session behind a client's cookie.
func sessionIDOf(t *testing.T, c *client) int64 {
	t.Helper()
	su, ok, err := c.srv.idx.SessionByToken(auth.HashToken(c.cookies[sessionCookie].Value))
	if err != nil || !ok {
		t.Fatalf("no session for the client: %v %v", ok, err)
	}
	return su.SessionID
}

func (c *client) backdate(d time.Duration) {
	c.t.Helper()
	if err := c.srv.idx.MarkSessionVerified(sessionIDOf(c.t, c), time.Now().Add(-d)); err != nil {
		c.t.Fatal(err)
	}
}

// confirm runs the reauth ceremony with a.
func (c *client) confirm(a *authtest.Authenticator) int {
	c.t.Helper()
	begin := mustCeremony(c.t, c.json("POST", "/auth/reauth/begin", nil))
	assertion, err := a.Assert(begin.Options, origin(c.srv))
	if err != nil {
		c.t.Fatal(err)
	}
	return c.do("POST", "/auth/reauth/finish?challenge="+url.QueryEscape(begin.ChallengeID), assertion).Code
}

func TestEveryStepUpRouteRefusesAStaleSession(t *testing.T) {
	w := newWorld(t)
	for _, rt := range w.srv.routeSpecs() {
		if !rt.fresh {
			continue
		}
		// carol is the admin, so admin routes get past the role check and the
		// refusal must come from the missing confirmation.
		if err := w.srv.idx.MarkSessionVerified(sessionIDFromCookie(t, w, w.carolC), time.Now().Add(-time.Hour)); err != nil {
			t.Fatal(err)
		}
		method, path := target(rt.pattern, w.bobVol.ID)
		path = strings.Replace(path, "/users/"+w.itoa(w.bobVol.ID), "/users/"+w.itoa(w.bob.ID), 1)
		path = strings.Replace(path, "/auth/passkeys/"+w.itoa(w.bobVol.ID), "/auth/passkeys/1", 1)
		rec := w.req(w.carolC, method, path, map[string]interface{}{"username": "x", "ownerId": w.ann.ID, "role": "user", "disabled": true})
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `"reauthRequired":true`) {
			t.Errorf("%s with a stale session = %d %s, want 403 reauthRequired", rt.pattern, rec.Code, rec.Body)
		}
	}
	// None of it happened.
	if u, _, _ := w.srv.idx.GetUser(w.bob.ID); u.Disabled || u.Username != "bob" {
		t.Fatalf("a refused request changed bob: %+v", u)
	}
	if vol, _, _ := w.srv.idx.GetVolume(w.bobVol.ID); *vol.OwnerID != w.bob.ID {
		t.Fatal("a refused request moved bob's volume")
	}
	if users, _ := w.srv.idx.ListUsers(); len(users) != 3 {
		t.Fatalf("a refused request created or deleted users: %d", len(users))
	}
}

func sessionIDFromCookie(t *testing.T, w *world, c *http.Cookie) int64 {
	t.Helper()
	su, ok, err := w.srv.idx.SessionByToken(auth.HashToken(c.Value))
	if err != nil || !ok {
		t.Fatalf("no session: %v %v", ok, err)
	}
	return su.SessionID
}

func TestAStaleSessionStillDoesEverythingElse(t *testing.T) {
	w := newWorld(t)
	w.srv.idx.MarkSessionVerified(sessionIDFromCookie(t, w, w.carolC), time.Now().Add(-24*time.Hour))
	for _, p := range []string{"/auth/me", "/auth/passkeys", "/auth/sessions", "/users", "/volumes", "/settings", "/search?q=x", "/stats"} {
		if rec := w.req(w.carolC, "GET", p, nil); rec.Code != http.StatusOK {
			t.Errorf("GET %s with a stale session = %d, want 200", p, rec.Code)
		}
	}
	if rec := w.req(w.carolC, "PUT", "/settings", map[string]interface{}{"content": map[string]bool{"paused": true}}); rec.Code != http.StatusOK {
		t.Errorf("changing settings = %d", rec.Code)
	}
}

func TestConfirmingWithAPasskeyUnlocksTheStepUpRoutes(t *testing.T) {
	srv := newBareServer(t)
	_, a, c := enrolled(t, srv, "ann", index.RoleUser)
	c.backdate(time.Hour)

	if rec := c.json("POST", "/auth/passkey/register/begin", nil); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "reauthRequired") {
		t.Fatalf("a stale session adding a passkey = %d %s", rec.Code, rec.Body)
	}
	if code := c.confirm(a); code != http.StatusNoContent {
		t.Fatalf("confirm = %d", code)
	}
	if rec := c.json("POST", "/auth/passkey/register/begin", nil); rec.Code != http.StatusOK {
		t.Fatalf("after confirming = %d %s", rec.Code, rec.Body)
	}

	// The window runs out again.
	c.backdate(StepUpWindow + time.Second)
	if rec := c.json("POST", "/auth/passkey/register/begin", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("after the window = %d", rec.Code)
	}
	// Confirming isn't signing in: enrolling and confirming never signed ann in.
	if u, _, _ := srv.idx.GetUserByName("ann"); u.LastLoginAt != "" {
		t.Fatalf("a confirmation stamped last login: %q", u.LastLoginAt)
	}
}

func TestOnlyTheUsersOwnPasskeyConfirms(t *testing.T) {
	srv := newBareServer(t)
	_, _, ann := enrolled(t, srv, "ann", index.RoleUser)
	_, bobKey, _ := enrolled(t, srv, "bob", index.RoleUser)
	ann.backdate(time.Hour)

	if code := ann.confirm(bobKey); code != http.StatusUnauthorized {
		t.Fatalf("ann confirming with bob's passkey = %d, want 401", code)
	}
	if code := ann.confirm(authtest.New()); code != http.StatusUnauthorized {
		t.Fatalf("an unregistered passkey = %d, want 401", code)
	}
	if rec := ann.json("POST", "/auth/passkey/register/begin", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("still stale after failed confirmations: %d", rec.Code)
	}
}

func TestConfirmationChallengesWorkOnceAndOnlyForTheirOwner(t *testing.T) {
	srv := newBareServer(t)
	_, annKey, ann := enrolled(t, srv, "ann", index.RoleUser)
	_, _, bob := enrolled(t, srv, "bob", index.RoleUser)
	ann.backdate(time.Hour)
	bob.backdate(time.Hour)

	begin := mustCeremony(t, ann.json("POST", "/auth/reauth/begin", nil))
	assertion, _ := annKey.Assert(begin.Options, origin(srv))
	target := "/auth/reauth/finish?challenge=" + url.QueryEscape(begin.ChallengeID)

	if rec := bob.do("POST", target, assertion); rec.Code != http.StatusBadRequest {
		t.Fatalf("bob finishing ann's confirmation = %d, want 400", rec.Code)
	}
	// bob taking it consumed it: even ann can't use it now.
	if rec := ann.do("POST", target, assertion); rec.Code != http.StatusBadRequest {
		t.Fatalf("a challenge someone else already took = %d", rec.Code)
	}

	begin = mustCeremony(t, ann.json("POST", "/auth/reauth/begin", nil))
	assertion, _ = annKey.Assert(begin.Options, origin(srv))
	target = "/auth/reauth/finish?challenge=" + url.QueryEscape(begin.ChallengeID)
	if rec := ann.do("POST", target, assertion); rec.Code != http.StatusNoContent {
		t.Fatalf("finish = %d %s", rec.Code, rec.Body)
	}
	if rec := ann.do("POST", target, assertion); rec.Code != http.StatusBadRequest {
		t.Fatalf("replaying a confirmation = %d, want 400", rec.Code)
	}
	// A login challenge can't be used to confirm.
	login := mustCeremony(t, newClient(t, srv).json("POST", "/auth/passkey/login/begin", nil))
	if rec := ann.do("POST", "/auth/reauth/finish?challenge="+login.ChallengeID, assertion); rec.Code != http.StatusBadRequest {
		t.Fatalf("a login challenge confirming = %d", rec.Code)
	}
}

func TestConfirmingRequiresASession(t *testing.T) {
	srv := newBareServer(t)
	c := newClient(t, srv)
	for _, p := range []string{"/auth/reauth/begin", "/auth/reauth/finish?challenge=x"} {
		if rec := c.json("POST", p, nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("POST %s signed out = %d, want 401", p, rec.Code)
		}
	}
}

func TestPasskeyRemovalNeedsAConfirmation(t *testing.T) {
	srv := newBareServer(t)
	u, a, c := enrolled(t, srv, "ann", index.RoleUser)
	// Add a second passkey so removing one is allowed in principle.
	second := authtest.New()
	begin := mustCeremony(t, c.json("POST", "/auth/passkey/register/begin", nil))
	cred, _ := second.Register(begin.Options, origin(srv))
	c.do("POST", "/auth/passkey/register/finish?challenge="+begin.ChallengeID, cred)
	creds, _ := srv.idx.ListCredentials(u.ID)
	target := "/auth/passkeys/" + strconv.FormatInt(creds[1].ID, 10)

	c.backdate(time.Hour)
	if rec := c.json("DELETE", target, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("removing a passkey from a stale session = %d", rec.Code)
	}
	if got, _ := srv.idx.ListCredentials(u.ID); len(got) != 2 {
		t.Fatal("the passkey was removed anyway")
	}
	if code := c.confirm(a); code != http.StatusNoContent {
		t.Fatalf("confirm = %d", code)
	}
	if rec := c.json("DELETE", target, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("after confirming = %d", rec.Code)
	}
}

// A new session is created by a passkey, so it starts confirmed; signing in or
// enrolling doesn't leave the next sensitive action asking again.
func TestSignInAndEnrollmentStartConfirmed(t *testing.T) {
	srv := newBareServer(t)
	_, a, enrolledClient := enrolled(t, srv, "ann", index.RoleUser)
	if rec := enrolledClient.json("POST", "/auth/passkey/register/begin", nil); rec.Code != http.StatusOK {
		t.Fatalf("right after enrolling = %d %s", rec.Code, rec.Body)
	}
	c := newClient(t, srv)
	c.signIn(a)
	if rec := c.json("POST", "/auth/passkey/register/begin", nil); rec.Code != http.StatusOK {
		t.Fatalf("right after signing in = %d %s", rec.Code, rec.Body)
	}
}

// ---- challenge flooding ----

func TestFloodingLoginBeginCannotKnockOutAnotherUsersSignIn(t *testing.T) {
	srv := newBareServer(t)
	_, a, _ := enrolled(t, srv, "ann", index.RoleUser)
	victim := newClient(t, srv)
	begin := mustCeremony(t, victim.json("POST", "/auth/passkey/login/begin", nil))
	assertion, _ := a.Assert(begin.Options, origin(srv))

	attacker := newClient(t, srv)
	attacker.addr = "198.51.100.9:1"
	for i := 0; i < 2100; i++ {
		attacker.json("POST", "/auth/passkey/login/begin", nil)
	}
	rec := victim.do("POST", "/auth/passkey/login/finish?challenge="+url.QueryEscape(begin.ChallengeID), assertion)
	if rec.Code != http.StatusOK {
		t.Fatalf("the victim's sign-in after a flood from another address = %d %s", rec.Code, rec.Body)
	}
}

func TestOnePassingClientHoldsAtMostAFewChallenges(t *testing.T) {
	srv := newBareServer(t)
	_, a, _ := enrolled(t, srv, "ann", index.RoleUser)
	c := newClient(t, srv)
	var first ceremony
	for i := 0; i < 30; i++ {
		b := mustCeremony(t, c.json("POST", "/auth/passkey/login/begin", nil))
		if i == 0 {
			first = b
		}
	}
	// Its first is long gone, but a fresh sign-in from the same address works.
	assertion, _ := a.Assert(first.Options, origin(srv))
	if rec := c.do("POST", "/auth/passkey/login/finish?challenge="+first.ChallengeID, assertion); rec.Code != http.StatusBadRequest {
		t.Fatalf("an evicted challenge = %d, want 400", rec.Code)
	}
	if rec := newClient(t, srv).signIn(a); rec.Code != http.StatusOK {
		t.Fatalf("a normal sign-in = %d", rec.Code)
	}
}

func TestAFullChallengeStoreAnswers503(t *testing.T) {
	srv := newBareServer(t)
	for i := 0; i < 5000; i++ {
		srv.challenges.Put("owner-"+strconv.Itoa(i), auth.Challenge{Kind: auth.KindLogin})
	}
	rec := newClient(t, srv).json("POST", "/auth/passkey/login/begin", nil)
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("with a full store: %d %s (Retry-After %q)", rec.Code, rec.Body, rec.Header().Get("Retry-After"))
	}
}

// ---- caching ----

func TestAuthenticatedResponsesAreNotCachedForSomeoneElse(t *testing.T) {
	w := newWorld(t)
	for _, rt := range w.srv.routeSpecs() {
		if rt.access == accessPublic {
			continue
		}
		method, path := target(rt.pattern, w.annVol.ID)
		if rt.access == accessVolumeQuery {
			path += "?volume=" + w.itoa(w.annVol.ID) + "&path=" + secretFile
		}
		// Signed out, and signed in as someone with no access, and as the owner:
		// whatever the answer, the browser must not keep it for anyone else.
		for who, c := range map[string]*http.Cookie{"signed out": nil, "another user": w.bobC, "the owner": w.annC} {
			if method != "GET" && method != "HEAD" && who == "the owner" {
				continue // don't act on the owner's data just to read a header
			}
			rec := w.req(c, method, path, map[string]string{})
			got := rec.Header().Get("Cache-Control")
			want := "private, no-store"
			if who == "the owner" && strings.HasPrefix(rt.pattern, "GET /files/content") && rec.Code == http.StatusOK {
				want = "private, no-cache"
			}
			if who == "the owner" && rt.pattern == "GET /filetypes" {
				want = "private, max-age=3600"
			}
			if got != want {
				t.Errorf("%s as %s (%d): Cache-Control %q, want %q", rt.pattern, who, rec.Code, got, want)
			}
		}
	}
}

func TestSignInResponsesAreNotCached(t *testing.T) {
	srv := newBareServer(t)
	u, _ := srv.idx.CreateUser("ann", index.RoleUser)
	c := newClient(t, srv)
	check := func(name string, rec interface{ Header() http.Header }) {
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("%s: Cache-Control %q, want no-store", name, got)
		}
	}
	check("login/begin", c.json("POST", "/auth/passkey/login/begin", nil))
	check("login/finish", c.do("POST", "/auth/passkey/login/finish?challenge=x", nil))
	check("enroll/begin", c.json("POST", "/auth/enroll/begin", map[string]string{"token": enrollmentToken(t, srv, u)}))
	check("enroll/finish", c.do("POST", "/auth/enroll/finish?challenge=x", nil))
}

func TestFileContentMustBeRevalidatedEachTime(t *testing.T) {
	w := newWorld(t)
	path := "/files/content?volume=" + w.itoa(w.annVol.ID) + "&path=" + url.QueryEscape("public/hello.txt")
	rec := w.req(w.annC, "GET", path, nil)
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "private, no-cache" {
		t.Fatalf("content = %d, Cache-Control %q", rec.Code, rec.Header().Get("Cache-Control"))
	}
	// Revalidation goes through the guard, so after the volume changes hands
	// the old owner's browser gets a refusal, not a "304 Not Modified".
	w.srv.idx.SetVolumeOwner(w.annVol.ID, w.bob.ID, func(old []byte) ([]byte, error) {
		pw, _ := openPassword(w.srv.masterKey, w.annVol.ID, &w.ann.ID, old)
		return sealPassword(w.srv.masterKey, w.annVol.ID, &w.bob.ID, pw)
	})
	r := httptestRequest(path, w.annC)
	r.Header.Set("If-Modified-Since", time.Now().UTC().Format(http.TimeFormat))
	out := serve(w, r)
	if out.Code != http.StatusNotFound {
		t.Fatalf("revalidating after losing access = %d, want 404", out.Code)
	}
}

func httptestRequest(path string, c *http.Cookie) *http.Request {
	r, _ := http.NewRequest("GET", path, nil)
	r.RemoteAddr = "192.0.2.50:1"
	r.AddCookie(c)
	return r
}

func serve(w *world, r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	w.srv.Routes().ServeHTTP(rec, r)
	return rec
}
