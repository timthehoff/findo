package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/thoff/findo/backend/internal/index"
)

// world is one server with three people: ann and bob (ordinary users) who each
// own a volume with files, and carol, an administrator who owns nothing.
type world struct {
	srv                *Server
	ann, bob, carol    index.User
	annC, bobC, carolC *http.Cookie
	annVol, bobVol     index.Volume
}

const secretFile = "secret/zeppelin-plan.txt"

func newWorld(t *testing.T) *world {
	t.Helper()
	srv := newBareServer(t)
	w := &world{srv: srv}
	w.ann, w.annC = signedInUser(t, srv, "ann", index.RoleUser)
	w.bob, w.bobC = signedInUser(t, srv, "bob", index.RoleUser)
	w.carol, w.carolC = signedInUser(t, srv, "carol", index.RoleAdmin)

	mk := func(owner index.User, name string, files map[string]string) index.Volume {
		vol, err := srv.idx.CreateVolume(owner.ID, name, "nas-"+name+".lan", "share-"+name, "user-"+name, func(id int64) ([]byte, error) {
			return sealPassword(srv.masterKey, id, &owner.ID, "pw-"+name)
		})
		if err != nil {
			t.Fatal(err)
		}
		smb := newFakeSMB()
		for p, body := range files {
			smb.setContent(p, []byte(body), 5)
		}
		_, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		srv.registerVolumeRuntime(vol.ID, smb, cancel)
		optIn(t, srv, vol.ID)
		return vol
	}
	w.annVol = mk(w.ann, "annvol", map[string]string{
		secretFile:         "The zeppelin merger plan is confidential.",
		"secret/notes.md":  "zeppelin budget notes",
		"public/hello.txt": "hello",
	})
	w.bobVol = mk(w.bob, "bobvol", map[string]string{"bob/diary.txt": "bob's own words"})
	for i := 0; i < 3; i++ { // let the indexer run until nothing is pending
		srv.contentPass(context.Background())
	}
	return w
}

func (w *world) req(c *http.Cookie, method, target string, body interface{}) *httptest.ResponseRecorder {
	var b []byte
	if body != nil {
		b, _ = json.Marshal(body)
	}
	r := httptest.NewRequest(method, target, bytes.NewReader(b))
	r.RemoteAddr = "192.0.2.50:1"
	if c != nil {
		r.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	w.srv.Routes().ServeHTTP(rec, r)
	return rec
}

// target turns a route pattern into a concrete request for volume id.
func target(pattern string, volumeID int64) (method, path string) {
	method, path, _ = strings.Cut(pattern, " ")
	id := strconv.FormatInt(volumeID, 10)
	path = strings.Replace(path, "{id}", id, 1)
	path = strings.Replace(path, "{name...}", "x", 1)
	return method, path
}

// The only routes anyone may call without signing in. Adding to this list is a
// decision to open a hole; it must be made on purpose.
var wantPublic = []string{
	"GET /healthz",
	"POST /auth/passkey/login/begin",
	"POST /auth/passkey/login/finish",
	"POST /auth/enroll/begin",
	"POST /auth/enroll/finish",
	"GET /enroll",      // the enrollment page
	"GET /search.html", // a redirect
	"GET /",            // the app's own static files
}

func TestOnlyTheSignInRoutesAreOpenToAnyone(t *testing.T) {
	srv := newBareServer(t)
	var got []string
	for _, rt := range srv.routeSpecs() {
		if rt.access == accessPublic {
			got = append(got, rt.pattern)
		}
	}
	sort.Strings(got)
	want := append([]string(nil), wantPublic...)
	sort.Strings(want)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("public routes are\n  %s\nwant exactly\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

func TestEveryRouteRefusesAnUnauthenticatedRequest(t *testing.T) {
	w := newWorld(t)
	seen := map[string]bool{}
	for _, rt := range w.srv.routeSpecs() {
		if seen[rt.pattern] {
			t.Errorf("route %q is declared twice", rt.pattern)
		}
		seen[rt.pattern] = true
		if rt.access == accessPublic {
			continue
		}
		method, path := target(rt.pattern, w.annVol.ID)
		if rt.access == accessVolumeQuery {
			path += "?volume=" + strconv.FormatInt(w.annVol.ID, 10) + "&path=" + secretFile
		}
		for name, cookie := range map[string]*http.Cookie{
			"no cookie":       nil,
			"a bogus cookie":  {Name: sessionCookie, Value: "bogus"},
			"another cookie":  {Name: "other", Value: "x"},
			"an empty cookie": {Name: sessionCookie, Value: ""},
		} {
			rec := w.req(cookie, method, path, map[string]string{})
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("%s with %s = %d %s, want 401", rt.pattern, name, rec.Code, rec.Body)
			}
			if strings.Contains(rec.Body.String(), "zeppelin") {
				t.Errorf("%s leaked data to an unauthenticated request", rt.pattern)
			}
		}
	}
}

func TestAdminRoutesRefuseOrdinaryUsers(t *testing.T) {
	w := newWorld(t)
	for _, rt := range w.srv.routeSpecs() {
		if rt.access != accessAdmin {
			continue
		}
		method, path := target(rt.pattern, w.annVol.ID)
		rec := w.req(w.bobC, method, path, map[string]interface{}{"username": "x", "ownerId": w.bob.ID, "role": "admin"})
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s as an ordinary user = %d %s, want 403", rt.pattern, rec.Code, rec.Body)
		}
	}
}

// The core promise: someone else's volume looks exactly like a volume that
// doesn't exist, on every route that takes one, even for an administrator.
func TestEveryVolumeRouteTreatsSomeoneElsesVolumeAsMissing(t *testing.T) {
	w := newWorld(t)
	var tested []string
	for _, rt := range w.srv.routeSpecs() {
		if rt.access != accessVolumePath && rt.access != accessVolumeQuery {
			continue
		}
		tested = append(tested, rt.pattern)
		call := func(c *http.Cookie, volumeID int64) *httptest.ResponseRecorder {
			method, path := target(rt.pattern, volumeID)
			if rt.access == accessVolumeQuery {
				path += "?volume=" + strconv.FormatInt(volumeID, 10) + "&path=" + secretFile + "&download=1"
			}
			return w.req(c, method, path, map[string]interface{}{
				"name": "stolen", "host": "evil.example.com", "share": "s", "username": "u", "password": "p",
			})
		}
		missing := call(w.bobC, 9999999)
		if missing.Code != http.StatusNotFound {
			t.Errorf("%s: a missing volume = %d, want 404", rt.pattern, missing.Code)
		}
		for who, c := range map[string]*http.Cookie{"another user": w.bobC, "an administrator": w.carolC} {
			rec := call(c, w.annVol.ID)
			if rec.Code != http.StatusNotFound {
				t.Errorf("%s as %s on ann's volume = %d %s, want 404", rt.pattern, who, rec.Code, rec.Body)
			}
			if rec.Body.String() != missing.Body.String() {
				t.Errorf("%s as %s: body %q differs from a missing volume's %q, so ids could be probed", rt.pattern, who, rec.Body, missing.Body)
			}
		}
	}
	for _, want := range []string{
		"GET /files", "GET /files/text", "GET /files/content", "GET /files/content/{name...}",
		"PUT /volumes/{id}", "DELETE /volumes/{id}", "POST /volumes/{id}/test", "POST /volumes/{id}/reindex",
		"POST /volumes/{id}/content/retry", "GET /volumes/{id}/crawl-runs", "GET /volumes/{id}/insights",
	} {
		found := false
		for _, p := range tested {
			found = found || p == want
		}
		if !found {
			t.Errorf("%s is not declared as taking a volume, so it isn't covered by the isolation checks", want)
		}
	}

	// None of it changed ann's volume, and she can still use every route.
	got, ok, _ := w.srv.idx.GetVolume(w.annVol.ID)
	if !ok || got.Name != "annvol" || got.Host != "nas-annvol.lan" || *got.OwnerID != w.ann.ID {
		t.Fatalf("ann's volume was altered: %+v", got)
	}
	for _, rt := range w.srv.routeSpecs() {
		if (rt.access != accessVolumePath && rt.access != accessVolumeQuery) || strings.HasPrefix(rt.pattern, "DELETE") || strings.HasPrefix(rt.pattern, "PUT") {
			continue
		}
		method, path := target(rt.pattern, w.annVol.ID)
		if rt.access == accessVolumeQuery {
			path += "?volume=" + strconv.FormatInt(w.annVol.ID, 10) + "&path=" + secretFile
		}
		if rec := w.req(w.annC, method, path, map[string]string{}); rec.Code == http.StatusNotFound && strings.Contains(rec.Body.String(), "volume not found") {
			t.Errorf("%s: the owner was refused her own volume", rt.pattern)
		}
	}
}

func TestSearchAndListingsNeverIncludeAnotherUsersFiles(t *testing.T) {
	w := newWorld(t)

	search := func(c *http.Cookie, q string) (names []string, snippets string, rec *httptest.ResponseRecorder) {
		rec = w.req(c, "GET", "/search?hidden=1&q="+q, nil)
		var body struct {
			Entries []struct {
				Path    string `json:"path"`
				Snippet string `json:"snippet"`
			} `json:"entries"`
		}
		decode(t, rec.Body.Bytes(), &body)
		for _, e := range body.Entries {
			names = append(names, e.Path)
			snippets += e.Snippet
		}
		return
	}

	// ann finds her file by name and by its text.
	if names, _, _ := search(w.annC, "zeppelin-plan"); len(names) != 1 || names[0] != secretFile {
		t.Fatalf("ann's name search: %v", names)
	}
	if names, snip, _ := search(w.annC, "merger"); len(names) != 1 || !strings.Contains(snip, "merger") {
		t.Fatalf("ann's content search: %v %q", names, snip)
	}

	for who, c := range map[string]*http.Cookie{"bob": w.bobC, "carol (admin)": w.carolC} {
		for _, q := range []string{"zeppelin", "zeppelin-plan", "merger", "confidential", "secret", "notes", "hello"} {
			names, snip, rec := search(c, q)
			if rec.Code != http.StatusOK || len(names) != 0 || snip != "" {
				t.Errorf("%s searching %q got %v %q (%d)", who, q, names, snip, rec.Code)
			}
		}
		// Naming ann's volume doesn't help.
		rec := w.req(c, "GET", "/search?q=zeppelin&volume="+strconv.FormatInt(w.annVol.ID, 10), nil)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s searching ann's volume = %d, want 404", who, rec.Code)
		}
	}
	// ...and bob does find his own.
	if names, _, _ := search(w.bobC, "diary"); len(names) != 1 {
		t.Errorf("bob's own search: %v", names)
	}
	// A search with no q and a date range is still scoped.
	rec := w.req(w.bobC, "GET", "/search?hidden=1&modified_after=1970-01-01", nil)
	if strings.Contains(rec.Body.String(), "zeppelin") || strings.Contains(rec.Body.String(), "hello") {
		t.Errorf("a date-only search leaked: %s", rec.Body)
	}
}

func TestAggregateEndpointsOnlyCountTheCallersOwnVolumes(t *testing.T) {
	w := newWorld(t)
	annID, bobID := strconv.FormatInt(w.annVol.ID, 10), strconv.FormatInt(w.bobVol.ID, 10)

	var ann, bob, carol struct{ FileCount, DirCount int }
	decode(t, w.req(w.annC, "GET", "/stats", nil).Body.Bytes(), &ann)
	decode(t, w.req(w.bobC, "GET", "/stats", nil).Body.Bytes(), &bob)
	decode(t, w.req(w.carolC, "GET", "/stats", nil).Body.Bytes(), &carol)
	if ann.FileCount != 3 || bob.FileCount != 1 || carol.FileCount != 0 {
		t.Errorf("file counts ann=%d bob=%d carol=%d, want 3, 1, 0 (each their own, the admin none)", ann.FileCount, bob.FileCount, carol.FileCount)
	}

	for _, c := range []struct {
		who    string
		cookie *http.Cookie
		sees   string
		hides  string
	}{{"bob", w.bobC, bobID, annID}, {"ann", w.annC, annID, bobID}} {
		health := w.req(c.cookie, "GET", "/health", nil).Body.String()
		if !strings.Contains(health, `"id":`+c.sees+`,`) || strings.Contains(health, `"id":`+c.hides+`,`) {
			t.Errorf("%s's /health: %s", c.who, health)
		}
		var status struct {
			Volumes []struct {
				VolumeID int64 `json:"volumeId"`
			}
		}
		decode(t, w.req(c.cookie, "GET", "/content/status", nil).Body.Bytes(), &status)
		if len(status.Volumes) != 1 || strconv.FormatInt(status.Volumes[0].VolumeID, 10) != c.sees {
			t.Errorf("%s's /content/status: %+v", c.who, status)
		}
	}
	if body := w.req(w.carolC, "GET", "/health", nil).Body.String(); strings.Contains(body, "annvol") || strings.Contains(body, "bobvol") {
		t.Errorf("the admin's /health lists others' volumes: %s", body)
	}
}

func TestVolumeListsShowOnlyYoursAndAdminsSeeNamesAndOwnersOfTheRest(t *testing.T) {
	w := newWorld(t)

	body := w.req(w.bobC, "GET", "/volumes", nil).Body.String()
	if !strings.Contains(body, "bobvol") || strings.Contains(body, "annvol") || strings.Contains(body, "nas-annvol") || strings.Contains(body, "others") {
		t.Errorf("bob's /volumes: %s", body)
	}

	rec := w.req(w.carolC, "GET", "/volumes", nil)
	var admin struct {
		Volumes []json.RawMessage
		Others  []map[string]interface{}
	}
	decode(t, rec.Body.Bytes(), &admin)
	if len(admin.Volumes) != 0 || len(admin.Others) != 2 {
		t.Fatalf("carol's /volumes: %s", rec.Body)
	}
	for _, o := range admin.Others {
		keys := []string{}
		for k := range o {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if strings.Join(keys, ",") != "id,name,ownerId,ownerName" {
			t.Errorf("an admin may see only id, name and owner of others' volumes, got %v", keys)
		}
	}
	for _, leak := range []string{"nas-annvol.lan", "share-annvol", "user-annvol", "pw-", "password"} {
		if strings.Contains(rec.Body.String(), leak) {
			t.Errorf("the admin's list leaks %q: %s", leak, rec.Body)
		}
	}
	if !strings.Contains(rec.Body.String(), `"ownerName":"ann"`) {
		t.Errorf("the admin should see who owns what: %s", rec.Body)
	}
}

func TestSettingsAreAdminOnly(t *testing.T) {
	w := newWorld(t)
	for _, c := range []*http.Cookie{w.annC, w.bobC} {
		if rec := w.req(c, "GET", "/settings", nil); rec.Code != http.StatusForbidden {
			t.Errorf("GET /settings as a user = %d", rec.Code)
		}
		if rec := w.req(c, "PUT", "/settings", map[string]interface{}{"content": map[string]bool{"paused": true}}); rec.Code != http.StatusForbidden {
			t.Errorf("PUT /settings as a user = %d", rec.Code)
		}
	}
	if cs, _ := w.srv.idx.ContentSettings(); cs.Paused {
		t.Fatal("an ordinary user paused content indexing")
	}
	if rec := w.req(w.carolC, "PUT", "/settings", map[string]interface{}{"content": map[string]bool{"paused": true}}); rec.Code != http.StatusOK {
		t.Errorf("PUT /settings as an admin = %d %s", rec.Code, rec.Body)
	}
}

func TestOnlyTheStaticAppIsServedWithoutSigningIn(t *testing.T) {
	w := newWorld(t)
	for _, p := range []string{"/", "/volumes.html", "/login.html", "/enroll", "/js/search.js", "/css/base.css"} {
		if rec := w.req(nil, "GET", p, nil); rec.Code != http.StatusOK {
			t.Errorf("GET %s signed out = %d, want the static file", p, rec.Code)
		}
	}
	if rec := w.req(nil, "GET", "/version", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("/version signed out = %d, want 401", rec.Code)
	}
}

func TestVolumeIdsInURLsAreValidatedBeforeAnythingElse(t *testing.T) {
	w := newWorld(t)
	for _, tc := range []struct {
		target string
		want   int
	}{
		{"/volumes/abc/insights", 400},
		{"/volumes/0/insights", 404},
		{"/volumes/-1/insights", 404},
		{"/files", 400},            // missing volume param
		{"/files?volume=abc", 400}, // not a number
		{"/files?volume=0", 404},   // nobody's
		{"/files?volume=1%20OR%201=1", 400},
	} {
		if rec := w.req(w.bobC, "GET", tc.target, nil); rec.Code != tc.want {
			t.Errorf("GET %s = %d %s, want %d", tc.target, rec.Code, rec.Body, tc.want)
		}
	}
}

// A user who owns nothing must get empty answers, never "everything".
func TestAUserWithNoVolumesSeesNothing(t *testing.T) {
	w := newWorld(t)
	_, newbie := signedInUser(t, w.srv, "newbie", index.RoleUser)

	for _, q := range []string{"zeppelin", "txt", "hello", "bob"} {
		var body struct {
			Entries []json.RawMessage
		}
		rec := w.req(newbie, "GET", "/search?hidden=1&q="+q, nil)
		decode(t, rec.Body.Bytes(), &body)
		if rec.Code != http.StatusOK || len(body.Entries) != 0 {
			t.Errorf("search %q for a user with no volumes = %d %s", q, rec.Code, rec.Body)
		}
	}
	for _, p := range []string{"/stats", "/health", "/content/status", "/volumes"} {
		rec := w.req(newbie, "GET", p, nil)
		for _, leak := range []string{"annvol", "bobvol", `"fileCount":1`, `"fileCount":3`, `"fileCount":4`} {
			if strings.Contains(rec.Body.String(), leak) {
				t.Errorf("GET %s for a user with no volumes leaked %q: %s", p, leak, rec.Body)
			}
		}
	}
}
