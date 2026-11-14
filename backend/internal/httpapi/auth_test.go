package httpapi

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/thoff/findo/backend/internal/auth"
	"github.com/thoff/findo/backend/internal/auth/authtest"
	"github.com/thoff/findo/backend/internal/crypto"
	"github.com/thoff/findo/backend/internal/index"
	"github.com/thoff/findo/backend/internal/testdb"
)

// newBareServer is a Server with nobody signed in and no volumes.
func newBareServer(t *testing.T) *Server {
	t.Helper()
	idx, err := index.Open(testdb.DSN(t))
	if err != nil {
		t.Fatalf("index.Open: %v", err)
	}
	t.Cleanup(func() { idx.Close() })
	srv := NewServer(idx, make([]byte, crypto.KeySize), "dev")
	srv.newSMB = func(host, share, user, pass string) smbConnector { return newFakeSMB() }
	return srv
}

// client is a browser: it keeps cookies between requests.
type client struct {
	t       *testing.T
	srv     *Server
	cookies map[string]*http.Cookie
	addr    string
	header  http.Header
}

func newClient(t *testing.T, srv *Server) *client {
	return &client{t: t, srv: srv, cookies: map[string]*http.Cookie{}, addr: "192.0.2.10:4321", header: http.Header{}}
}

func (c *client) do(method, target string, body []byte) *httptest.ResponseRecorder {
	c.t.Helper()
	req := httptest.NewRequest(method, target, bytes.NewReader(body))
	req.RemoteAddr = c.addr
	for k, v := range c.header {
		req.Header[k] = v
	}
	for _, ck := range c.cookies {
		req.AddCookie(ck)
	}
	rec := httptest.NewRecorder()
	c.srv.Routes().ServeHTTP(rec, req)
	for _, ck := range rec.Result().Cookies() {
		if ck.MaxAge < 0 || ck.Value == "" {
			delete(c.cookies, ck.Name)
		} else {
			c.cookies[ck.Name] = ck
		}
	}
	return rec
}

func (c *client) json(method, target string, v interface{}) *httptest.ResponseRecorder {
	c.t.Helper()
	var b []byte
	if v != nil {
		b, _ = json.Marshal(v)
	}
	return c.do(method, target, b)
}

func (c *client) signedIn() bool { return c.cookies[sessionCookie] != nil }

func origin(srv *Server) string { return srv.publicURL.Scheme + "://" + srv.publicURL.Host }

type ceremony struct {
	ChallengeID string          `json:"challengeId"`
	Options     json.RawMessage `json:"options"`
}

func mustCeremony(t *testing.T, rec *httptest.ResponseRecorder) ceremony {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("begin = %d %s", rec.Code, rec.Body)
	}
	var c ceremony
	decode(t, rec.Body.Bytes(), &c)
	return c
}

// enrollmentToken issues a link for u and returns its token (the URL fragment).
func enrollmentToken(t *testing.T, srv *Server, u index.User) string {
	t.Helper()
	link, _, err := auth.IssueEnrollment(srv.idx, srv.publicURL, u.ID, index.EnrollFirst, nil)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(link)
	if err != nil || parsed.Path != "/enroll" || parsed.Fragment == "" {
		t.Fatalf("link %q must be <public url>/enroll#<token>", link)
	}
	return parsed.Fragment
}

// enroll registers a first passkey for u with link token, returning the
// finish response. The client ends up signed in on success.
func (c *client) enroll(token string, a *authtest.Authenticator, label string) *httptest.ResponseRecorder {
	c.t.Helper()
	begin := mustCeremony(c.t, c.json("POST", "/auth/enroll/begin", map[string]string{"token": token, "label": label}))
	cred, err := a.Register(begin.Options, origin(c.srv))
	if err != nil {
		c.t.Fatal(err)
	}
	return c.do("POST", "/auth/enroll/finish?challenge="+url.QueryEscape(begin.ChallengeID), cred)
}

// signIn runs a discoverable sign-in with a.
func (c *client) signIn(a *authtest.Authenticator) *httptest.ResponseRecorder {
	c.t.Helper()
	begin := mustCeremony(c.t, c.json("POST", "/auth/passkey/login/begin", nil))
	assertion, err := a.Assert(begin.Options, origin(c.srv))
	if err != nil {
		c.t.Fatal(err)
	}
	return c.do("POST", "/auth/passkey/login/finish?challenge="+url.QueryEscape(begin.ChallengeID), assertion)
}

// enrolled makes a user with one registered passkey and returns them with the
// authenticator, with a signed-in client.
func enrolled(t *testing.T, srv *Server, name, role string) (index.User, *authtest.Authenticator, *client) {
	t.Helper()
	u, err := srv.idx.CreateUser(name, role)
	if err != nil {
		t.Fatal(err)
	}
	a := authtest.New()
	c := newClient(t, srv)
	if rec := c.enroll(enrollmentToken(t, srv, u), a, "test device"); rec.Code != http.StatusCreated {
		t.Fatalf("enroll = %d %s", rec.Code, rec.Body)
	}
	return u, a, c
}

func TestEnrollmentRegistersAPasskeyAndSignsTheUserIn(t *testing.T) {
	srv := newBareServer(t)
	u, _ := srv.idx.CreateUser("ann", index.RoleUser)
	a := authtest.New()
	c := newClient(t, srv)

	rec := c.enroll(enrollmentToken(t, srv, u), a, "ann's phone")
	if rec.Code != http.StatusCreated {
		t.Fatalf("enroll = %d %s", rec.Code, rec.Body)
	}
	var out struct {
		User    map[string]interface{} `json:"user"`
		Passkey index.Credential       `json:"passkey"`
	}
	decode(t, rec.Body.Bytes(), &out)
	if out.User["username"] != "ann" || out.Passkey.Label != "ann's phone" || !c.signedIn() {
		t.Fatalf("response %+v, signed in %v", out, c.signedIn())
	}
	if strings.Contains(rec.Body.String(), "publicKey") || strings.Contains(rec.Body.String(), "credentialId") {
		t.Errorf("a passkey's key material must not be returned: %s", rec.Body)
	}

	me := c.json("GET", "/auth/me", nil)
	if me.Code != 200 || !strings.Contains(me.Body.String(), `"ann"`) {
		t.Fatalf("/auth/me = %d %s", me.Code, me.Body)
	}
	creds, _ := srv.idx.ListCredentials(u.ID)
	if len(creds) != 1 || creds[0].Label != "ann's phone" {
		t.Fatalf("stored passkeys: %+v", creds)
	}
}

func TestEnrollmentLinkWorksOnceAndOnlyWhenValid(t *testing.T) {
	srv := newBareServer(t)
	u, _ := srv.idx.CreateUser("ann", index.RoleUser)
	token := enrollmentToken(t, srv, u)

	if rec := newClient(t, srv).enroll(token, authtest.New(), ""); rec.Code != http.StatusCreated {
		t.Fatalf("first use = %d %s", rec.Code, rec.Body)
	}
	for name, tok := range map[string]string{"a used link": token, "a made-up token": "AAAA", "an empty token": ""} {
		rec := newClient(t, srv).json("POST", "/auth/enroll/begin", map[string]string{"token": tok})
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid or has expired") {
			t.Errorf("%s: %d %s, want 400 with one generic message", name, rec.Code, rec.Body)
		}
	}

	// An expired link.
	hash := auth.HashToken("expired-token")
	srv.idx.CreateEnrollment(u.ID, hash, index.EnrollRecovery, nil, -time.Minute)
	if rec := newClient(t, srv).json("POST", "/auth/enroll/begin", map[string]string{"token": "expired-token"}); rec.Code != http.StatusBadRequest {
		t.Errorf("an expired link = %d", rec.Code)
	}

	// Two registrations racing on one link: only one wins.
	token2 := enrollmentToken(t, srv, u)
	c1, c2 := newClient(t, srv), newClient(t, srv)
	b1 := mustCeremony(t, c1.json("POST", "/auth/enroll/begin", map[string]string{"token": token2}))
	b2 := mustCeremony(t, c2.json("POST", "/auth/enroll/begin", map[string]string{"token": token2}))
	cred1, _ := authtest.New().Register(b1.Options, origin(srv))
	cred2, _ := authtest.New().Register(b2.Options, origin(srv))
	r1 := c1.do("POST", "/auth/enroll/finish?challenge="+b1.ChallengeID, cred1)
	r2 := c2.do("POST", "/auth/enroll/finish?challenge="+b2.ChallengeID, cred2)
	if r1.Code != http.StatusCreated || r2.Code == http.StatusCreated {
		t.Errorf("racing finishes = %d, %d; want exactly the first to succeed", r1.Code, r2.Code)
	}
}

func TestEnrollmentRefusesAnAuthenticatorWithoutUserVerification(t *testing.T) {
	srv := newBareServer(t)
	u, _ := srv.idx.CreateUser("ann", index.RoleUser)
	weak := authtest.New()
	weak.SkipUserVerification = true
	c := newClient(t, srv)
	if rec := c.enroll(enrollmentToken(t, srv, u), weak, ""); rec.Code == http.StatusCreated || c.signedIn() {
		t.Fatalf("a passkey that skips user verification was accepted: %d %s", rec.Code, rec.Body)
	}
	if creds, _ := srv.idx.ListCredentials(u.ID); len(creds) != 0 {
		t.Fatalf("stored %+v", creds)
	}
}

func TestEnrollmentRefusesAWrongOrigin(t *testing.T) {
	srv := newBareServer(t)
	u, _ := srv.idx.CreateUser("ann", index.RoleUser)
	c := newClient(t, srv)
	begin := mustCeremony(t, c.json("POST", "/auth/enroll/begin", map[string]string{"token": enrollmentToken(t, srv, u)}))
	cred, _ := authtest.New().Register(begin.Options, "https://evil.example.com")
	if rec := c.do("POST", "/auth/enroll/finish?challenge="+begin.ChallengeID, cred); rec.Code != http.StatusUnauthorized {
		t.Fatalf("a registration from another origin = %d %s, want 401", rec.Code, rec.Body)
	}
}

func TestSignInWithADiscoverablePasskeyNeedsNoUsername(t *testing.T) {
	srv := newBareServer(t)
	_, a, _ := enrolled(t, srv, "ann", index.RoleAdmin)
	enrolled(t, srv, "bob", index.RoleUser) // another user, so the right one must be picked

	c := newClient(t, srv)
	rec := c.signIn(a)
	if rec.Code != http.StatusOK || !c.signedIn() {
		t.Fatalf("sign-in = %d %s", rec.Code, rec.Body)
	}
	me := c.json("GET", "/auth/me", nil)
	if !strings.Contains(me.Body.String(), `"ann"`) || !strings.Contains(me.Body.String(), `"admin"`) {
		t.Fatalf("signed in as %s", me.Body)
	}
	if u, _, _ := srv.idx.GetUserByName("ann"); u.LastLoginAt == "" {
		t.Error("last login not recorded")
	}
	ann, _, _ := srv.idx.GetUserByName("ann")
	creds, _ := srv.idx.ListCredentials(ann.ID)
	if creds[0].SignCount != 1 || creds[0].LastUsedAt == "" {
		t.Errorf("one sign-in should leave the counter at 1 and stamp last use: %+v", creds[0])
	}
}

func TestSignInIsRefusedWithOneGenericMessage(t *testing.T) {
	srv := newBareServer(t)
	u, a, _ := enrolled(t, srv, "ann", index.RoleUser)

	attempt := func(name string, mutate func(begin ceremony) (cred []byte, challenge string)) *httptest.ResponseRecorder {
		c := newClient(t, srv)
		begin := mustCeremony(t, c.json("POST", "/auth/passkey/login/begin", nil))
		cred, ch := mutate(begin)
		rec := c.do("POST", "/auth/passkey/login/finish?challenge="+url.QueryEscape(ch), cred)
		if c.signedIn() {
			t.Errorf("%s: signed in", name)
		}
		return rec
	}
	cases := map[string]func(ceremony) ([]byte, string){
		"a passkey nobody registered": func(b ceremony) ([]byte, string) {
			stranger := authtest.New()
			stranger.UserHandle = auth.Handle(u.ID)
			cred, _ := stranger.Assert(b.Options, origin(srv))
			return cred, b.ChallengeID
		},
		"the wrong origin": func(b ceremony) ([]byte, string) {
			cred, _ := a.Assert(b.Options, "https://evil.example.com")
			return cred, b.ChallengeID
		},
		"someone else's user handle": func(b ceremony) ([]byte, string) {
			cred, _ := a.AssertWithHandle(b.Options, origin(srv), auth.Handle(u.ID+100))
			return cred, b.ChallengeID
		},
		"garbage": func(b ceremony) ([]byte, string) { return []byte(`{"not":"a credential"}`), b.ChallengeID },
	}
	for name, mutate := range cases {
		rec := attempt(name, mutate)
		if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "sign-in failed") {
			t.Errorf("%s: %d %s, want 401 \"sign-in failed\"", name, rec.Code, rec.Body)
		}
		// A distinct limiter key per attempt isn't needed: failures are free until 5.
	}
}

func TestEachChallengeWorksOnce(t *testing.T) {
	srv := newBareServer(t)
	_, a, _ := enrolled(t, srv, "ann", index.RoleUser)
	c := newClient(t, srv)
	begin := mustCeremony(t, c.json("POST", "/auth/passkey/login/begin", nil))
	assertion, _ := a.Assert(begin.Options, origin(srv))
	target := "/auth/passkey/login/finish?challenge=" + url.QueryEscape(begin.ChallengeID)

	if rec := c.do("POST", target, assertion); rec.Code != http.StatusOK {
		t.Fatalf("first finish = %d %s", rec.Code, rec.Body)
	}
	other := newClient(t, srv)
	if rec := other.do("POST", target, assertion); rec.Code != http.StatusBadRequest || other.signedIn() {
		t.Fatalf("replaying a captured sign-in = %d %s, want 400 and no session", rec.Code, rec.Body)
	}
	if rec := other.do("POST", "/auth/passkey/login/finish?challenge=bogus", assertion); rec.Code != http.StatusBadRequest {
		t.Fatalf("an unknown challenge = %d", rec.Code)
	}
	// A challenge of one kind can't finish another.
	begin2 := mustCeremony(t, newClient(t, srv).json("POST", "/auth/passkey/login/begin", nil))
	if rec := newClient(t, srv).do("POST", "/auth/enroll/finish?challenge="+begin2.ChallengeID, assertion); rec.Code != http.StatusBadRequest {
		t.Fatalf("a login challenge used to enroll = %d", rec.Code)
	}
}

func TestDisabledUsersCannotSignIn(t *testing.T) {
	srv := newBareServer(t)
	u, a, c := enrolled(t, srv, "ann", index.RoleUser)
	yes := true
	srv.idx.UpdateUser(u.ID, nil, &yes)

	if rec := c.json("GET", "/auth/me", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("a disabled user's existing session = %d, want 401", rec.Code)
	}
	fresh := newClient(t, srv)
	if rec := fresh.signIn(a); rec.Code != http.StatusUnauthorized || fresh.signedIn() {
		t.Fatalf("sign-in as a disabled user = %d %s", rec.Code, rec.Body)
	}
	no := false
	srv.idx.UpdateUser(u.ID, nil, &no)
	if rec := newClient(t, srv).signIn(a); rec.Code != http.StatusOK {
		t.Fatalf("after re-enabling = %d %s", rec.Code, rec.Body)
	}
}

func TestAClonedAuthenticatorIsLoggedButNotRefused(t *testing.T) {
	var logs bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(prev) })

	srv := newBareServer(t)
	_, a, _ := enrolled(t, srv, "ann", index.RoleUser)
	if rec := newClient(t, srv).signIn(a); rec.Code != http.StatusOK {
		t.Fatal(rec.Body)
	}
	a.SignCount = 0 // a second copy of the passkey that is behind
	c := newClient(t, srv)
	if rec := c.signIn(a); rec.Code != http.StatusOK || !c.signedIn() {
		t.Fatalf("a non-increasing counter is a warning, not a refusal: %d %s", rec.Code, rec.Body)
	}
	if !strings.Contains(logs.String(), "cloned") {
		t.Errorf("the regression wasn't logged:\n%s", logs.String())
	}
	if !strings.Contains(logs.String(), "sign-in ok user=ann") {
		t.Errorf("successful sign-ins should be logged:\n%s", logs.String())
	}
}

func TestFailedAttemptsAreLoggedAndThenThrottledPerAddress(t *testing.T) {
	var logs bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(prev) })

	srv := newBareServer(t)
	_, a, _ := enrolled(t, srv, "ann", index.RoleUser)

	attacker := newClient(t, srv)
	attacker.addr = "198.51.100.7:1111"
	for i := 0; i < 6; i++ {
		begin := mustCeremony(t, attacker.json("POST", "/auth/passkey/login/begin", nil))
		rec := attacker.do("POST", "/auth/passkey/login/finish?challenge="+begin.ChallengeID, []byte(`{}`))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d = %d", i+1, rec.Code)
		}
	}
	rec := attacker.json("POST", "/auth/passkey/login/begin", nil)
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("after repeated failures: %d %s (Retry-After %q), want 429", rec.Code, rec.Body, rec.Header().Get("Retry-After"))
	}
	if rec := attacker.json("POST", "/auth/enroll/begin", map[string]string{"token": "x"}); rec.Code != http.StatusTooManyRequests {
		t.Errorf("enrollment shares the brake: %d", rec.Code)
	}
	if !strings.Contains(logs.String(), "sign-in failed ip=198.51.100.7") {
		t.Errorf("failures should be logged with the address:\n%s", logs.String())
	}

	// A different address is untouched, and a real sign-in still works for it.
	if rec := newClient(t, srv).signIn(a); rec.Code != http.StatusOK {
		t.Fatalf("another address = %d %s", rec.Code, rec.Body)
	}
}

func TestRequestsWithoutASessionAreToldToSignIn(t *testing.T) {
	srv := newBareServer(t)
	c := newClient(t, srv)

	rec := c.json("GET", "/stats", nil)
	var body map[string]interface{}
	decode(t, rec.Body.Bytes(), &body)
	if rec.Code != http.StatusUnauthorized || body["setupRequired"] != true || !strings.Contains(body["error"].(string), "findo-server user add --admin") {
		t.Fatalf("with no accounts: %d %s; want 401 explaining the CLI command", rec.Code, rec.Body)
	}

	srv.idx.CreateUser("ann", index.RoleAdmin)
	rec = c.json("GET", "/stats", nil)
	body = nil
	decode(t, rec.Body.Bytes(), &body)
	if rec.Code != http.StatusUnauthorized || body["setupRequired"] != nil || body["error"] != "sign in required" {
		t.Fatalf("with accounts: %d %s", rec.Code, rec.Body)
	}

	c.cookies[sessionCookie] = &http.Cookie{Name: sessionCookie, Value: "not-a-session"}
	if rec := c.json("GET", "/stats", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("a bogus cookie = %d", rec.Code)
	}
}

func TestSessionCookieFlags(t *testing.T) {
	srv := newBareServer(t)
	u, a, _ := enrolled(t, srv, "ann", index.RoleUser)
	_ = u

	cookieFor := func(c *client) *http.Cookie {
		rec := c.signIn(a)
		for _, ck := range rec.Result().Cookies() {
			if ck.Name == sessionCookie {
				return ck
			}
		}
		t.Fatal("no session cookie set")
		return nil
	}

	plain := cookieFor(newClient(t, srv))
	if !plain.HttpOnly || plain.SameSite != http.SameSiteLaxMode || plain.Path != "/" || plain.Secure {
		t.Errorf("over plain http: %+v", plain)
	}
	if plain.MaxAge != int(index.SessionTTL.Seconds()) {
		t.Errorf("MaxAge = %d, want 30 days", plain.MaxAge)
	}

	// Behind a TLS proxy on this machine, the cookie is Secure.
	proxied := newClient(t, srv)
	proxied.addr = "127.0.0.1:5555"
	proxied.header.Set("X-Forwarded-Proto", "https")
	if ck := cookieFor(proxied); !ck.Secure || !ck.HttpOnly {
		t.Errorf("behind a local proxy: %+v", ck)
	}

	// X-Forwarded-Proto from anywhere else is just a claim.
	spoofed := newClient(t, srv)
	spoofed.header.Set("X-Forwarded-Proto", "https")
	if ck := cookieFor(spoofed); ck.Secure {
		t.Errorf("a remote client's X-Forwarded-Proto must be ignored: %+v", ck)
	}

	// An https public URL makes it Secure regardless.
	pu, _ := url.Parse("https://findo.example.com")
	srv.SetPublicURL(pu)
	c := newClient(t, srv)
	begin := mustCeremony(t, c.json("POST", "/auth/passkey/login/begin", nil))
	assertion, _ := a.Assert(begin.Options, "https://findo.example.com")
	rec := c.do("POST", "/auth/passkey/login/finish?challenge="+begin.ChallengeID, assertion)
	if rec.Code != http.StatusOK || c.cookies[sessionCookie] == nil || !c.cookies[sessionCookie].Secure {
		t.Errorf("an https public URL must make the cookie Secure: %d %s %+v", rec.Code, rec.Body, c.cookies[sessionCookie])
	}
}

func TestClientIPTrustsForwardedForOnlyFromLoopback(t *testing.T) {
	req := func(remote, xff string) *http.Request {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = remote
		if xff != "" {
			r.Header.Set("X-Forwarded-For", xff)
		}
		return r
	}
	for _, c := range []struct{ remote, xff, want string }{
		{"203.0.113.5:1", "", "203.0.113.5"},
		{"203.0.113.5:1", "10.0.0.1", "203.0.113.5"},             // a client's claim
		{"127.0.0.1:1", "198.51.100.2", "198.51.100.2"},          // the proxy's
		{"127.0.0.1:1", "6.6.6.6, 198.51.100.2", "198.51.100.2"}, // last entry is the proxy's
		{"[::1]:1", "198.51.100.2", "198.51.100.2"},
		{"127.0.0.1:1", "garbage", "127.0.0.1"},
		{"127.0.0.1:1", "", "127.0.0.1"},
	} {
		if got := clientIP(req(c.remote, c.xff)); got != c.want {
			t.Errorf("clientIP(%s, %q) = %s, want %s", c.remote, c.xff, got, c.want)
		}
	}
}

func TestLogoutEndsTheSession(t *testing.T) {
	srv := newBareServer(t)
	_, _, c := enrolled(t, srv, "ann", index.RoleUser)
	stolen := *c.cookies[sessionCookie]

	if rec := c.json("POST", "/auth/logout", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("logout = %d", rec.Code)
	}
	if c.signedIn() {
		t.Error("the cookie should be cleared")
	}
	thief := newClient(t, srv)
	thief.cookies[sessionCookie] = &stolen
	if rec := thief.json("GET", "/auth/me", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("the old token after logout = %d, want 401", rec.Code)
	}
}

func TestAnExpiredSessionIsRefused(t *testing.T) {
	srv := newBareServer(t)
	u, _ := srv.idx.CreateUser("ann", index.RoleUser)
	token, hash := auth.NewToken()
	srv.idx.CreateSession(u.ID, nil, hash, -time.Minute, "", "")
	c := newClient(t, srv)
	c.cookies[sessionCookie] = &http.Cookie{Name: sessionCookie, Value: token}
	if rec := c.json("GET", "/auth/me", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("an expired session = %d", rec.Code)
	}
}

func TestPasskeysCanBeAddedRenamedAndRemoved(t *testing.T) {
	srv := newBareServer(t)
	u, first, c := enrolled(t, srv, "ann", index.RoleUser)

	// Add a second passkey while signed in.
	second := authtest.New()
	begin := mustCeremony(t, c.json("POST", "/auth/passkey/register/begin", map[string]string{"label": "laptop"}))
	cred, err := second.Register(begin.Options, origin(srv))
	if err != nil {
		t.Fatal(err)
	}
	rec := c.do("POST", "/auth/passkey/register/finish?challenge="+begin.ChallengeID, cred)
	if rec.Code != http.StatusCreated {
		t.Fatalf("register/finish = %d %s", rec.Code, rec.Body)
	}
	var added struct{ Passkey index.Credential }
	decode(t, rec.Body.Bytes(), &added)

	// Registering the same authenticator again is refused (it's excluded).
	again := mustCeremony(t, c.json("POST", "/auth/passkey/register/begin", nil))
	if _, err := second.Register(again.Options, origin(srv)); err == nil {
		t.Error("the server should exclude passkeys the user already has")
	}

	list := c.json("GET", "/auth/passkeys", nil)
	var listed struct{ Passkeys []index.Credential }
	decode(t, list.Body.Bytes(), &listed)
	if len(listed.Passkeys) != 2 || listed.Passkeys[1].Label != "laptop" {
		t.Fatalf("passkeys: %s", list.Body)
	}

	// Either passkey signs in.
	if r := newClient(t, srv).signIn(second); r.Code != http.StatusOK {
		t.Fatalf("signing in with the second passkey = %d %s", r.Code, r.Body)
	}

	target := "/auth/passkeys/" + strconv.FormatInt(added.Passkey.ID, 10)
	if r := c.json("PUT", target, map[string]string{"label": "work laptop"}); r.Code != http.StatusNoContent {
		t.Fatalf("rename = %d %s", r.Code, r.Body)
	}
	if r := c.json("PUT", target, map[string]string{"label": "  "}); r.Code != http.StatusBadRequest {
		t.Errorf("an empty label = %d", r.Code)
	}

	// The session that the second passkey made ends when it is removed.
	viaSecond := newClient(t, srv)
	viaSecond.signIn(second)
	if r := c.json("DELETE", target, nil); r.Code != http.StatusNoContent {
		t.Fatalf("delete = %d %s", r.Code, r.Body)
	}
	if r := viaSecond.json("GET", "/auth/me", nil); r.Code != http.StatusUnauthorized {
		t.Errorf("a removed passkey's session = %d, want 401", r.Code)
	}
	if r := c.json("GET", "/auth/me", nil); r.Code != http.StatusOK {
		t.Errorf("the other passkey's session = %d, want 200", r.Code)
	}
	if r := newClient(t, srv).signIn(second); r.Code == http.StatusOK {
		t.Error("a removed passkey still signs in")
	}

	// The last one can't be removed.
	creds, _ := srv.idx.ListCredentials(u.ID)
	last := "/auth/passkeys/" + strconv.FormatInt(creds[0].ID, 10)
	if r := c.json("DELETE", last, nil); r.Code != http.StatusConflict {
		t.Fatalf("deleting the last passkey = %d %s, want 409", r.Code, r.Body)
	}
	if r := newClient(t, srv).signIn(first); r.Code != http.StatusOK {
		t.Fatalf("the last passkey must still work: %d", r.Code)
	}
}

func TestPasskeyEndpointsOnlyTouchTheCallersOwn(t *testing.T) {
	srv := newBareServer(t)
	ann, _, annClient := enrolled(t, srv, "ann", index.RoleUser)
	_, _, bobClient := enrolled(t, srv, "bob", index.RoleUser)
	annKeys, _ := srv.idx.ListCredentials(ann.ID)
	target := "/auth/passkeys/" + strconv.FormatInt(annKeys[0].ID, 10)

	if r := bobClient.json("DELETE", target, nil); r.Code != http.StatusNotFound {
		t.Errorf("bob deleting ann's passkey = %d, want 404", r.Code)
	}
	if r := bobClient.json("PUT", target, map[string]string{"label": "mine now"}); r.Code != http.StatusNotFound {
		t.Errorf("bob renaming ann's passkey = %d, want 404", r.Code)
	}
	if got := bobClient.json("GET", "/auth/passkeys", nil).Body.String(); strings.Contains(got, "test device") && strings.Count(got, `"id"`) != 1 {
		t.Errorf("bob sees more than his own passkey: %s", got)
	}

	// A register challenge begun by ann can't be finished by bob.
	begin := mustCeremony(t, annClient.json("POST", "/auth/passkey/register/begin", nil))
	cred, _ := authtest.New().Register(begin.Options, origin(srv))
	if r := bobClient.do("POST", "/auth/passkey/register/finish?challenge="+begin.ChallengeID, cred); r.Code == http.StatusCreated {
		t.Error("bob finished ann's registration")
	}
	if creds, _ := srv.idx.ListCredentials(ann.ID); len(creds) != 1 {
		t.Errorf("ann's passkeys changed: %+v", creds)
	}
}

func TestSessionsAreListedAndCanBeEnded(t *testing.T) {
	srv := newBareServer(t)
	u, a, here := enrolled(t, srv, "ann", index.RoleUser)
	there := newClient(t, srv)
	there.header.Set("User-Agent", "Safari on a phone")
	there.signIn(a)
	_, _, bob := enrolled(t, srv, "bob", index.RoleUser)

	var listed struct {
		Sessions []struct {
			ID        int64  `json:"id"`
			Current   bool   `json:"current"`
			UserAgent string `json:"userAgent"`
		}
	}
	decode(t, here.json("GET", "/auth/sessions", nil).Body.Bytes(), &listed)
	if len(listed.Sessions) != 2 {
		t.Fatalf("sessions: %+v", listed)
	}
	var other int64
	for _, s := range listed.Sessions {
		if !s.Current {
			other = s.ID
			if s.UserAgent != "Safari on a phone" {
				t.Errorf("user agent %q", s.UserAgent)
			}
		}
	}

	if r := bob.json("DELETE", "/auth/sessions/"+strconv.FormatInt(other, 10), nil); r.Code != http.StatusNotFound {
		t.Errorf("bob ending ann's session = %d, want 404", r.Code)
	}
	if r := here.json("DELETE", "/auth/sessions/"+strconv.FormatInt(other, 10), nil); r.Code != http.StatusNoContent {
		t.Fatalf("ending another session = %d", r.Code)
	}
	if r := there.json("GET", "/auth/me", nil); r.Code != http.StatusUnauthorized {
		t.Errorf("an ended session = %d, want 401", r.Code)
	}
	if r := here.json("GET", "/auth/me", nil); r.Code != http.StatusOK {
		t.Errorf("this session = %d", r.Code)
	}
	_ = u
}

func TestHealthzIsPublicAndTellsNothing(t *testing.T) {
	srv := newBareServer(t)
	rec := newClient(t, srv).json("GET", "/healthz", nil)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"status":"ok"}` {
		t.Fatalf("/healthz = %d %s", rec.Code, rec.Body)
	}
}
