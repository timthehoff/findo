package httpapi

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/thoff/findo/backend/internal/auth"
	"github.com/thoff/findo/backend/internal/auth/authtest"
	"github.com/thoff/findo/backend/internal/index"
)

// join runs the invite ceremony with authenticator a, returning the response
// of whichever step ended it.
func (c *client) join(token, username string, a *authtest.Authenticator) *httptest.ResponseRecorder {
	c.t.Helper()
	rec := c.json("POST", "/auth/invite/begin", map[string]string{"token": token, "username": username, "label": "test device"})
	if rec.Code != http.StatusOK {
		return rec
	}
	var begin ceremony
	decode(c.t, rec.Body.Bytes(), &begin)
	cred, err := a.Register(begin.Options, origin(c.srv))
	if err != nil {
		c.t.Fatal(err)
	}
	return c.do("POST", "/auth/invite/finish?challenge="+url.QueryEscape(begin.ChallengeID), cred)
}

type madeInvite struct {
	ID    int64
	Token string
	Link  string
}

// newInvite has the admin client make an invite and returns its token.
func newInvite(t *testing.T, admin *client, body map[string]interface{}) madeInvite {
	t.Helper()
	rec := admin.json("POST", "/invites", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create invite = %d %s", rec.Code, rec.Body)
	}
	var out struct {
		Invite index.Invite `json:"invite"`
		Link   string       `json:"link"`
	}
	decode(t, rec.Body.Bytes(), &out)
	u, err := url.Parse(out.Link)
	if err != nil || u.Path != "/join" || u.Fragment == "" {
		t.Fatalf("link %q must be <public url>/join#<token>", out.Link)
	}
	return madeInvite{out.Invite.ID, u.Fragment, out.Link}
}

func inviteState(t *testing.T, admin *client, id int64) index.Invite {
	t.Helper()
	var out struct{ Invites []index.Invite }
	decode(t, admin.json("GET", "/invites", nil).Body.Bytes(), &out)
	for _, v := range out.Invites {
		if v.ID == id {
			return v
		}
	}
	t.Fatalf("invite %d not listed", id)
	return index.Invite{}
}

func TestAnInviteLetsSomeoneMakeTheirOwnAccount(t *testing.T) {
	srv := newBareServer(t)
	_, _, admin := enrolled(t, srv, "carol", index.RoleAdmin)
	inv := newInvite(t, admin, map[string]interface{}{"label": "family", "expiresInHours": 24, "maxUses": 2})
	if !strings.HasPrefix(inv.Link, "http://localhost:8080/join#") {
		t.Fatalf("link = %q", inv.Link)
	}

	dora := authtest.New()
	c := newClient(t, srv)
	rec := c.join(inv.Token, "dora", dora)
	if rec.Code != http.StatusCreated || !c.signedIn() {
		t.Fatalf("join = %d %s", rec.Code, rec.Body)
	}
	var out struct {
		User    map[string]interface{}
		Passkey index.Credential
	}
	decode(t, rec.Body.Bytes(), &out)
	if out.User["username"] != "dora" || out.User["role"] != index.RoleUser || out.Passkey.Label != "test device" {
		t.Fatalf("response: %s", rec.Body)
	}
	if strings.Contains(rec.Body.String(), "publicKey") || strings.Contains(rec.Body.String(), inv.Token) {
		t.Errorf("the response leaks key material or the token: %s", rec.Body)
	}

	// An ordinary user, with nothing of anyone else's.
	if r := c.json("GET", "/users", nil); r.Code != http.StatusForbidden {
		t.Errorf("a joined user listing users = %d, want 403", r.Code)
	}
	if r := c.json("GET", "/volumes", nil); r.Code != http.StatusOK || strings.Contains(r.Body.String(), `"others"`) {
		t.Errorf("their volumes: %d %s", r.Code, r.Body)
	}
	// The passkey was registered for the real account id, so a plain sign-in
	// (which finds the account by the handle stored on the passkey) works.
	if r := newClient(t, srv).signIn(dora); r.Code != http.StatusOK {
		t.Fatalf("signing in later with the passkey = %d %s", r.Code, r.Body)
	}

	// The admin sees where the account came from.
	var users struct{ Users []index.UserSummary }
	decode(t, admin.json("GET", "/users", nil).Body.Bytes(), &users)
	var via string
	for _, u := range users.Users {
		if u.Username == "dora" {
			via = u.InviteLabel
		}
	}
	if via != "family" {
		t.Errorf("dora's invite label = %q, want family", via)
	}
	if v := inviteState(t, admin, inv.ID); v.Uses != 1 || v.Status != "active" {
		t.Errorf("after one of two uses: %+v", v)
	}
}

func TestAnInviteWorksForItsNumberOfUses(t *testing.T) {
	srv := newBareServer(t)
	_, _, admin := enrolled(t, srv, "carol", index.RoleAdmin)
	inv := newInvite(t, admin, map[string]interface{}{"maxUses": 2})

	for _, name := range []string{"one", "two"} {
		if rec := newClient(t, srv).join(inv.Token, name, authtest.New()); rec.Code != http.StatusCreated {
			t.Fatalf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	rec := newClient(t, srv).join(inv.Token, "three", authtest.New())
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid, used up or has expired") {
		t.Fatalf("a third use = %d %s", rec.Code, rec.Body)
	}
	if _, ok, _ := srv.idx.GetUserByName("three"); ok {
		t.Fatal("an account was made past the limit")
	}
	if v := inviteState(t, admin, inv.ID); v.Status != "used up" || v.Uses != 2 {
		t.Errorf("state: %+v", v)
	}
}

func TestDefaultsAndLimitsOfAnInvite(t *testing.T) {
	srv := newBareServer(t)
	_, _, admin := enrolled(t, srv, "carol", index.RoleAdmin)

	inv := newInvite(t, admin, map[string]interface{}{})
	v := inviteState(t, admin, inv.ID)
	exp, _ := time.Parse(time.RFC3339, v.ExpiresAt)
	if v.MaxUses != 1 || time.Until(exp) < 6*24*time.Hour || time.Until(exp) > 8*24*time.Hour {
		t.Errorf("defaults should be one use and about seven days: %+v", v)
	}
	for name, body := range map[string]map[string]interface{}{
		"too long an expiry": {"expiresInHours": 721},
		"a negative expiry":  {"expiresInHours": -5},
		"too many uses":      {"maxUses": 101},
		"negative uses":      {"maxUses": -1},
		"a huge label":       {"label": strings.Repeat("x", 61)},
	} {
		if rec := admin.json("POST", "/invites", body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s = %d %s, want 400", name, rec.Code, rec.Body)
		}
	}
	if rec := admin.json("POST", "/invites", map[string]interface{}{"expiresInHours": 720, "maxUses": 100}); rec.Code != http.StatusCreated {
		t.Errorf("the largest allowed invite = %d %s", rec.Code, rec.Body)
	}
}

func TestOnlyAdministratorsManageInvites(t *testing.T) {
	srv := newBareServer(t)
	_, _, admin := enrolled(t, srv, "carol", index.RoleAdmin)
	_, _, user := enrolled(t, srv, "bob", index.RoleUser)
	inv := newInvite(t, admin, map[string]interface{}{})

	for name, rec := range map[string]*httptest.ResponseRecorder{
		"list":   user.json("GET", "/invites", nil),
		"create": user.json("POST", "/invites", map[string]interface{}{}),
		"revoke": user.json("DELETE", "/invites/"+strconv.FormatInt(inv.ID, 10), nil),
	} {
		if rec.Code != http.StatusForbidden {
			t.Errorf("a user trying to %s an invite = %d, want 403", name, rec.Code)
		}
	}
	if v := inviteState(t, admin, inv.ID); v.Status != "active" {
		t.Fatalf("a user revoked an invite: %+v", v)
	}
	// An invite list never contains the means of using one.
	body := admin.json("GET", "/invites", nil).Body.String()
	for _, leak := range []string{inv.Token, "token", "link", "hash"} {
		if strings.Contains(strings.ToLower(body), leak) {
			t.Errorf("the invite list contains %q: %s", leak, body)
		}
	}
}

func TestCreatingAnInviteNeedsAFreshPasskeyConfirmation(t *testing.T) {
	srv := newBareServer(t)
	_, key, admin := enrolled(t, srv, "carol", index.RoleAdmin)
	admin.backdate(time.Hour)

	if rec := admin.json("POST", "/invites", map[string]interface{}{}); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "reauthRequired") {
		t.Fatalf("a stale session making an invite = %d %s", rec.Code, rec.Body)
	}
	if invites, _ := srv.idx.ListInvites(); len(invites) != 0 {
		t.Fatal("an invite was made anyway")
	}
	if code := admin.confirm(key); code != http.StatusNoContent {
		t.Fatal(code)
	}
	newInvite(t, admin, map[string]interface{}{})
	// Revoking is allowed without: ending access should always be quick.
	admin.backdate(time.Hour)
	inv := inviteState(t, admin, func() int64 { l, _ := srv.idx.ListInvites(); return l[0].ID }())
	if rec := admin.json("DELETE", "/invites/"+strconv.FormatInt(inv.ID, 10), nil); rec.Code != http.StatusNoContent {
		t.Errorf("revoking from a stale session = %d, want 204", rec.Code)
	}
}

func TestRevokingAnInviteStopsJoins(t *testing.T) {
	srv := newBareServer(t)
	_, _, admin := enrolled(t, srv, "carol", index.RoleAdmin)
	inv := newInvite(t, admin, map[string]interface{}{"maxUses": 5})
	early := newClient(t, srv)
	if rec := early.join(inv.Token, "early", authtest.New()); rec.Code != http.StatusCreated {
		t.Fatal(rec.Body)
	}

	if rec := admin.json("DELETE", "/invites/"+strconv.FormatInt(inv.ID, 10), nil); rec.Code != http.StatusNoContent {
		t.Fatalf("revoke = %d", rec.Code)
	}
	if rec := newClient(t, srv).join(inv.Token, "late", authtest.New()); rec.Code != http.StatusBadRequest {
		t.Fatalf("a join after revoking = %d %s", rec.Code, rec.Body)
	}
	if r := early.json("GET", "/auth/me", nil); r.Code != http.StatusOK {
		t.Errorf("revoking an invite signed out someone who had already joined: %d", r.Code)
	}
	if rec := admin.json("DELETE", "/invites/999999", nil); rec.Code != http.StatusNotFound {
		t.Errorf("revoking nothing = %d", rec.Code)
	}
}

func TestInvalidInviteLinksAreRefusedWithOneMessageAndThrottled(t *testing.T) {
	srv := newBareServer(t)
	admin, _ := srv.idx.CreateUser("carol", index.RoleAdmin)
	expiredToken, expiredHash := auth.NewToken()
	srv.idx.CreateInvite("old", expiredHash, admin.ID, -time.Minute, 1)

	probe := newClient(t, srv)
	probe.addr = "198.51.100.77:1"
	for name, tok := range map[string]string{"made up": "AAAA", "empty": "", "expired": expiredToken} {
		rec := probe.json("POST", "/auth/invite/begin", map[string]string{"token": tok, "username": "x"})
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid, used up or has expired") {
			t.Errorf("%s: %d %s, want 400 with one generic message", name, rec.Code, rec.Body)
		}
	}
	for i := 0; i < 5; i++ {
		probe.json("POST", "/auth/invite/begin", map[string]string{"token": "guess", "username": "x"})
	}
	rec := probe.json("POST", "/auth/invite/begin", map[string]string{"token": "guess", "username": "x"})
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("after repeated bad links: %d (Retry-After %q), want 429", rec.Code, rec.Header().Get("Retry-After"))
	}
}

func TestUsernamesAreCheckedWithoutSpendingAUse(t *testing.T) {
	srv := newBareServer(t)
	_, _, admin := enrolled(t, srv, "carol", index.RoleAdmin)
	inv := newInvite(t, admin, map[string]interface{}{})

	c := newClient(t, srv)
	for name, tc := range map[string]struct {
		username string
		want     int
	}{
		"empty":                {"", http.StatusBadRequest},
		"a space":              {"a b", http.StatusBadRequest},
		"a slash":              {"a/b", http.StatusBadRequest},
		"starting with a dash": {"-x", http.StatusBadRequest},
		"too long":             {strings.Repeat("a", 65), http.StatusBadRequest},
		"taken (any case)":     {"CAROL", http.StatusConflict},
	} {
		if rec := c.json("POST", "/auth/invite/begin", map[string]string{"token": inv.Token, "username": tc.username}); rec.Code != tc.want {
			t.Errorf("%s: %d %s, want %d", name, rec.Code, rec.Body, tc.want)
		}
	}
	if v := inviteState(t, admin, inv.ID); v.Uses != 0 {
		t.Fatalf("rejected names spent a use: %+v", v)
	}
	// Whitespace around a good name is trimmed.
	if rec := newClient(t, srv).join(inv.Token, "  erin  ", authtest.New()); rec.Code != http.StatusCreated {
		t.Fatalf("a good name = %d %s", rec.Code, rec.Body)
	}
	if _, ok, _ := srv.idx.GetUserByName("erin"); !ok {
		t.Fatal("the trimmed name wasn't used")
	}
}

func TestGuessingTakenNamesWithAnInviteIsThrottled(t *testing.T) {
	srv := newBareServer(t)
	_, _, admin := enrolled(t, srv, "carol", index.RoleAdmin)
	inv := newInvite(t, admin, map[string]interface{}{"maxUses": 5})

	probe := newClient(t, srv)
	probe.addr = "198.51.100.78:1"
	for i := 0; i < 6; i++ {
		probe.json("POST", "/auth/invite/begin", map[string]string{"token": inv.Token, "username": "carol"})
	}
	rec := probe.json("POST", "/auth/invite/begin", map[string]string{"token": inv.Token, "username": "anyone"})
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("after repeated taken names = %d, want 429 (otherwise a link holder can list who has an account)", rec.Code)
	}
	if rec := newClient(t, srv).join(inv.Token, "someone", authtest.New()); rec.Code != http.StatusCreated {
		t.Fatalf("another address is unaffected: %d %s", rec.Code, rec.Body)
	}
}

func TestAJoinThatFailsVerificationSpendsNothingAndLeavesNoAccount(t *testing.T) {
	srv := newBareServer(t)
	_, _, admin := enrolled(t, srv, "carol", index.RoleAdmin)
	inv := newInvite(t, admin, map[string]interface{}{})

	weak := authtest.New()
	weak.SkipUserVerification = true
	if rec := newClient(t, srv).join(inv.Token, "weak", weak); rec.Code != http.StatusBadRequest {
		t.Fatalf("a passkey without user verification = %d %s", rec.Code, rec.Body)
	}
	// wrong origin
	c := newClient(t, srv)
	begin := mustCeremony(t, c.json("POST", "/auth/invite/begin", map[string]string{"token": inv.Token, "username": "evil"}))
	cred, _ := authtest.New().Register(begin.Options, "https://evil.example.com")
	if rec := c.do("POST", "/auth/invite/finish?challenge="+begin.ChallengeID, cred); rec.Code != http.StatusBadRequest {
		t.Fatalf("a registration from another origin = %d", rec.Code)
	}
	for _, name := range []string{"weak", "evil"} {
		if _, ok, _ := srv.idx.GetUserByName(name); ok {
			t.Errorf("a failed join left the account %q", name)
		}
	}
	if v := inviteState(t, admin, inv.ID); v.Uses != 0 {
		t.Fatalf("failed joins spent a use: %+v", v)
	}
	if rec := newClient(t, srv).join(inv.Token, "good", authtest.New()); rec.Code != http.StatusCreated {
		t.Fatalf("the invite should still work: %d %s", rec.Code, rec.Body)
	}
}

func TestJoinChallengesWorkOnceAndOnlyAsJoinChallenges(t *testing.T) {
	srv := newBareServer(t)
	_, _, admin := enrolled(t, srv, "carol", index.RoleAdmin)
	inv := newInvite(t, admin, map[string]interface{}{"maxUses": 3})

	a := authtest.New()
	c := newClient(t, srv)
	begin := mustCeremony(t, c.json("POST", "/auth/invite/begin", map[string]string{"token": inv.Token, "username": "frank"}))
	cred, _ := a.Register(begin.Options, origin(srv))
	target := "/auth/invite/finish?challenge=" + url.QueryEscape(begin.ChallengeID)

	// The enrollment endpoint can't finish it, and trying burns it.
	if rec := newClient(t, srv).do("POST", "/auth/enroll/finish?challenge="+begin.ChallengeID, cred); rec.Code != http.StatusBadRequest {
		t.Fatalf("a join challenge finishing an enrollment = %d", rec.Code)
	}
	if rec := c.do("POST", target, cred); rec.Code != http.StatusBadRequest {
		t.Fatalf("the challenge should have been used up: %d", rec.Code)
	}

	// Conversely, an enrollment challenge can't make an account.
	u, _ := srv.idx.CreateUser("grace", index.RoleUser)
	eb := mustCeremony(t, newClient(t, srv).json("POST", "/auth/enroll/begin", map[string]string{"token": enrollmentToken(t, srv, u)}))
	ecred, _ := authtest.New().Register(eb.Options, origin(srv))
	if rec := newClient(t, srv).do("POST", "/auth/invite/finish?challenge="+eb.ChallengeID, ecred); rec.Code != http.StatusBadRequest {
		t.Fatalf("an enrollment challenge finishing a join = %d", rec.Code)
	}

	// And a good one works once; replaying it is refused and makes no second account.
	good := newClient(t, srv)
	b2 := mustCeremony(t, good.json("POST", "/auth/invite/begin", map[string]string{"token": inv.Token, "username": "henry"}))
	c2, _ := authtest.New().Register(b2.Options, origin(srv))
	t2 := "/auth/invite/finish?challenge=" + url.QueryEscape(b2.ChallengeID)
	if rec := good.do("POST", t2, c2); rec.Code != http.StatusCreated {
		t.Fatalf("finish = %d %s", rec.Code, rec.Body)
	}
	if rec := newClient(t, srv).do("POST", t2, c2); rec.Code != http.StatusBadRequest {
		t.Fatalf("replaying a join = %d", rec.Code)
	}
	if v := inviteState(t, admin, inv.ID); v.Uses != 1 {
		t.Fatalf("uses = %d, want 1", v.Uses)
	}
}

func TestTwoPeopleCannotTakeTheSameNameAtOnce(t *testing.T) {
	srv := newBareServer(t)
	_, _, admin := enrolled(t, srv, "carol", index.RoleAdmin)
	inv := newInvite(t, admin, map[string]interface{}{"maxUses": 2})

	// Both pass the name check (nobody has it yet), then race to finish.
	a, b := newClient(t, srv), newClient(t, srv)
	ba := mustCeremony(t, a.json("POST", "/auth/invite/begin", map[string]string{"token": inv.Token, "username": "same"}))
	bb := mustCeremony(t, b.json("POST", "/auth/invite/begin", map[string]string{"token": inv.Token, "username": "same"}))
	ca, _ := authtest.New().Register(ba.Options, origin(srv))
	cb, _ := authtest.New().Register(bb.Options, origin(srv))
	ra := a.do("POST", "/auth/invite/finish?challenge="+ba.ChallengeID, ca)
	rb := b.do("POST", "/auth/invite/finish?challenge="+bb.ChallengeID, cb)
	if ra.Code != http.StatusCreated || rb.Code != http.StatusConflict {
		t.Fatalf("finishes = %d, %d; want the first to win and the second to get 409", ra.Code, rb.Code)
	}
	if v := inviteState(t, admin, inv.ID); v.Uses != 1 {
		t.Fatalf("the loser spent a use: %+v", v)
	}
}

func TestJoinResponsesAreNotCached(t *testing.T) {
	srv := newBareServer(t)
	c := newClient(t, srv)
	for name, rec := range map[string]*httptest.ResponseRecorder{
		"begin":  c.json("POST", "/auth/invite/begin", map[string]string{"token": "x", "username": "y"}),
		"finish": c.do("POST", "/auth/invite/finish?challenge=x", nil),
	} {
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("%s: Cache-Control %q, want no-store", name, got)
		}
	}
}

func TestTheJoinPageIsServedToAnyone(t *testing.T) {
	srv := newBareServer(t)
	rec := newClient(t, srv).do("GET", "/join", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "You're invited") {
		t.Fatalf("GET /join = %d", rec.Code)
	}
	if !strings.Contains(rec.Header().Get("Content-Security-Policy"), "default-src 'self'") {
		t.Error("the join page needs the strict CSP like every other page")
	}
}
