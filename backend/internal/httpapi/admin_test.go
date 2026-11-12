package httpapi

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/thoff/findo/backend/internal/auth/authtest"
	"github.com/thoff/findo/backend/internal/index"
)

func (w *world) itoa(id int64) string { return strconv.FormatInt(id, 10) }

func TestAdminCreatesAUserAndTheLinkEnrolls(t *testing.T) {
	w := newWorld(t)
	rec := w.req(w.carolC, "POST", "/users", map[string]string{"username": "dave", "role": "user"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	var out struct {
		User           index.User `json:"user"`
		EnrollmentLink string     `json:"enrollmentLink"`
		ExpiresAt      string     `json:"expiresAt"`
	}
	decode(t, rec.Body.Bytes(), &out)
	if out.User.Username != "dave" || out.User.Role != index.RoleUser || out.ExpiresAt == "" {
		t.Fatalf("response: %+v", out)
	}
	link, err := url.Parse(out.EnrollmentLink)
	if err != nil || link.Path != "/enroll" || link.Fragment == "" || link.Host != "localhost:8080" {
		t.Fatalf("link %q should be <public url>/enroll#token", out.EnrollmentLink)
	}

	dave := newClient(t, w.srv)
	if rec := dave.enroll(link.Fragment, authtest.New(), "dave's phone"); rec.Code != http.StatusCreated || !dave.signedIn() {
		t.Fatalf("enrolling dave = %d %s", rec.Code, rec.Body)
	}
	if rec := dave.json("GET", "/volumes", nil); rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "annvol") {
		t.Errorf("a new user's volumes: %d %s", rec.Code, rec.Body)
	}
	if rec := dave.json("GET", "/users", nil); rec.Code != http.StatusForbidden {
		t.Errorf("dave listing users = %d, want 403", rec.Code)
	}
}

func TestCreatingAUserValidatesTheRequest(t *testing.T) {
	w := newWorld(t)
	for name, tc := range map[string]struct {
		body map[string]string
		want int
	}{
		"a duplicate name (any case)": {map[string]string{"username": "ANN"}, http.StatusConflict},
		"an empty name":               {map[string]string{"username": " "}, http.StatusBadRequest},
		"a name with a space":         {map[string]string{"username": "a b"}, http.StatusBadRequest},
		"an unknown role":             {map[string]string{"username": "eve", "role": "root"}, http.StatusBadRequest},
	} {
		if rec := w.req(w.carolC, "POST", "/users", tc.body); rec.Code != tc.want {
			t.Errorf("%s = %d %s, want %d", name, rec.Code, rec.Body, tc.want)
		}
	}
	// No role means an ordinary user, never an admin.
	rec := w.req(w.carolC, "POST", "/users", map[string]string{"username": "frank"})
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"role":"user"`) {
		t.Errorf("default role: %d %s", rec.Code, rec.Body)
	}
}

func TestUsersListShowsPasskeyAndVolumeCounts(t *testing.T) {
	w := newWorld(t)
	var out struct{ Users []index.UserSummary }
	decode(t, w.req(w.carolC, "GET", "/users", nil).Body.Bytes(), &out)
	if len(out.Users) != 3 {
		t.Fatalf("users: %+v", out.Users)
	}
	for _, u := range out.Users {
		wantVols := map[string]int{"ann": 1, "bob": 1, "carol": 0}[u.Username]
		if u.Volumes != wantVols || u.Passkeys != 0 {
			t.Errorf("%s: %+v", u.Username, u)
		}
	}
	if body := w.req(w.carolC, "GET", "/users", nil).Body.String(); strings.Contains(body, "token") || strings.Contains(body, "publicKey") {
		t.Errorf("the user list must carry no secrets: %s", body)
	}
}

func TestAdminDisablesAndEnablesUsersAndChangesRoles(t *testing.T) {
	w := newWorld(t)
	target := "/users/" + w.itoa(w.bob.ID)

	if rec := w.req(w.carolC, "PUT", target, map[string]interface{}{"disabled": true}); rec.Code != http.StatusOK {
		t.Fatalf("disable = %d %s", rec.Code, rec.Body)
	}
	if rec := w.req(w.bobC, "GET", "/auth/me", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("a disabled user's session = %d, want 401", rec.Code)
	}
	if rec := w.req(w.carolC, "PUT", target, map[string]interface{}{"disabled": false, "role": "admin"}); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"role":"admin"`) {
		t.Fatalf("enable + promote = %d %s", rec.Code, rec.Body)
	}
	if rec := w.req(w.carolC, "PUT", target, map[string]interface{}{"role": "wizard"}); rec.Code != http.StatusBadRequest {
		t.Errorf("an unknown role = %d", rec.Code)
	}
	if rec := w.req(w.carolC, "PUT", "/users/999999", map[string]interface{}{"disabled": true}); rec.Code != http.StatusNotFound {
		t.Errorf("an unknown user = %d", rec.Code)
	}
}

func TestAnAdminCannotLockThemselvesOut(t *testing.T) {
	w := newWorld(t)
	self := "/users/" + w.itoa(w.carol.ID)
	for name, body := range map[string]map[string]interface{}{
		"demote yourself":  {"role": "user"},
		"disable yourself": {"disabled": true},
	} {
		if rec := w.req(w.carolC, "PUT", self, body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s = %d %s, want 400", name, rec.Code, rec.Body)
		}
	}
	if rec := w.req(w.carolC, "DELETE", self, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("delete yourself = %d", rec.Code)
	}
	// Re-stating your own role is harmless.
	if rec := w.req(w.carolC, "PUT", self, map[string]interface{}{"role": "admin", "disabled": false}); rec.Code != http.StatusOK {
		t.Errorf("a no-op self update = %d %s", rec.Code, rec.Body)
	}
}

func TestDeletingAUserRemovesTheirVolumesAndFiles(t *testing.T) {
	w := newWorld(t)
	if w.srv.volumeRuntime(w.annVol.ID) == nil {
		t.Fatal("setup: ann's volume should be running")
	}
	if rec := w.req(w.carolC, "DELETE", "/users/"+w.itoa(w.ann.ID), nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d %s", rec.Code, rec.Body)
	}
	if _, ok, _ := w.srv.idx.GetUser(w.ann.ID); ok {
		t.Error("the user is still there")
	}
	if _, ok, _ := w.srv.idx.GetVolume(w.annVol.ID); ok {
		t.Error("their volume is still configured")
	}
	if w.srv.volumeRuntime(w.annVol.ID) != nil {
		t.Error("their volume is still connected")
	}
	if st, _ := w.srv.idx.Stats([]int64{w.annVol.ID}); st.FileCount != 0 {
		t.Errorf("their indexed files remain: %+v", st)
	}
	if rec := w.req(w.annC, "GET", "/auth/me", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("their session = %d, want 401", rec.Code)
	}
	if vols, _ := w.srv.idx.ListVolumesByOwner(w.bob.ID); len(vols) != 1 {
		t.Errorf("another user's volumes were affected: %+v", vols)
	}
	if rec := w.req(w.carolC, "DELETE", "/users/"+w.itoa(w.ann.ID), nil); rec.Code != http.StatusNotFound {
		t.Errorf("deleting twice = %d", rec.Code)
	}
}

func TestAdminIssuesEnrollmentLinksForRecovery(t *testing.T) {
	w := newWorld(t)
	_, oldKey, annClient := enrolled(t, w.srv, "dora", index.RoleUser)
	dora, _, _ := w.srv.idx.GetUserByName("dora")
	issue := func() string {
		rec := w.req(w.carolC, "POST", "/users/"+w.itoa(dora.ID)+"/enrollment", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("issue = %d %s", rec.Code, rec.Body)
		}
		var out struct{ EnrollmentLink string }
		decode(t, rec.Body.Bytes(), &out)
		l, _ := url.Parse(out.EnrollmentLink)
		return l.Fragment
	}

	first, second := issue(), issue()
	if rec := newClient(t, w.srv).json("POST", "/auth/enroll/begin", map[string]string{"token": first}); rec.Code != http.StatusBadRequest {
		t.Errorf("an older link should stop working once a newer one is issued: %d", rec.Code)
	}
	newKey := authtest.New()
	fresh := newClient(t, w.srv)
	if rec := fresh.enroll(second, newKey, "replacement"); rec.Code != http.StatusCreated {
		t.Fatalf("recovery enrollment = %d %s", rec.Code, rec.Body)
	}
	creds, _ := w.srv.idx.ListCredentials(dora.ID)
	if len(creds) != 2 {
		t.Fatalf("recovery adds a passkey, it doesn't replace: %+v", creds)
	}
	// Both passkeys sign in; the lost one can then be removed from the account page.
	for name, k := range map[string]*authtest.Authenticator{"old": oldKey, "new": newKey} {
		if rec := newClient(t, w.srv).signIn(k); rec.Code != http.StatusOK {
			t.Errorf("%s passkey sign-in = %d", name, rec.Code)
		}
	}
	_ = annClient

	yes := true
	w.srv.idx.UpdateUser(dora.ID, nil, &yes)
	if rec := w.req(w.carolC, "POST", "/users/"+w.itoa(dora.ID)+"/enrollment", nil); rec.Code != http.StatusConflict {
		t.Errorf("a link for a disabled user = %d, want 409", rec.Code)
	}
	if rec := w.req(w.carolC, "POST", "/users/999999/enrollment", nil); rec.Code != http.StatusNotFound {
		t.Errorf("a link for nobody = %d", rec.Code)
	}
}

func TestReassigningAVolumeMovesItAndItsPasswordToTheNewOwner(t *testing.T) {
	w := newWorld(t)
	ownerURL := "/volumes/" + w.itoa(w.annVol.ID) + "/owner"

	if rec := w.req(w.carolC, "PUT", ownerURL, map[string]int64{"ownerId": 99999}); rec.Code != http.StatusBadRequest {
		t.Errorf("an unknown owner = %d", rec.Code)
	}
	if rec := w.req(w.carolC, "PUT", "/volumes/99999/owner", map[string]int64{"ownerId": w.bob.ID}); rec.Code != http.StatusNotFound {
		t.Errorf("an unknown volume = %d", rec.Code)
	}
	if rec := w.req(w.carolC, "PUT", ownerURL, map[string]int64{"ownerId": w.bob.ID}); rec.Code != http.StatusOK {
		t.Fatalf("reassign = %d %s", rec.Code, rec.Body)
	}

	if rec := w.req(w.annC, "GET", "/files?volume="+w.itoa(w.annVol.ID), nil); rec.Code != http.StatusNotFound {
		t.Errorf("the old owner still has access: %d", rec.Code)
	}
	if rec := w.req(w.bobC, "GET", "/files?volume="+w.itoa(w.annVol.ID), nil); rec.Code != http.StatusOK {
		t.Errorf("the new owner has no access: %d %s", rec.Code, rec.Body)
	}
	// The password was resealed for the new owner: the volume still connects.
	secret, _ := w.srv.idx.GetVolumeSecret(w.annVol.ID)
	if pw, err := openPassword(w.srv.masterKey, secret.ID, secret.OwnerID, secret.PasswordEnc); err != nil || pw != "pw-annvol" {
		t.Fatalf("password after the move = %q, %v", pw, err)
	}
	vol, _, _ := w.srv.idx.GetVolume(w.annVol.ID)
	if err := w.srv.StartVolume(context.Background(), vol); err != nil {
		t.Fatalf("the moved volume no longer starts: %v", err)
	}

	// bob now has two volumes with distinct names; a clash is refused.
	w.req(w.bobC, "PUT", "/volumes/"+w.itoa(w.bobVol.ID), map[string]interface{}{"name": "annvol", "host": w.bobVol.Host, "share": w.bobVol.Share, "username": w.bobVol.Username})
	if rec := w.req(w.carolC, "PUT", ownerURL, map[string]int64{"ownerId": w.ann.ID}); rec.Code != http.StatusOK {
		t.Fatalf("moving it back = %d %s", rec.Code, rec.Body)
	}
}

func TestAVolumePasswordOnlyOpensForItsOwnVolumeAndOwner(t *testing.T) {
	w := newWorld(t)
	annSecret, _ := w.srv.idx.GetVolumeSecret(w.annVol.ID)

	if _, err := openPassword(w.srv.masterKey, w.annVol.ID, &w.ann.ID, annSecret.PasswordEnc); err != nil {
		t.Fatalf("the right volume and owner: %v", err)
	}
	for name, try := range map[string]func() error{
		"another owner": func() error {
			_, err := openPassword(w.srv.masterKey, w.annVol.ID, &w.bob.ID, annSecret.PasswordEnc)
			return err
		},
		"another volume": func() error {
			_, err := openPassword(w.srv.masterKey, w.bobVol.ID, &w.ann.ID, annSecret.PasswordEnc)
			return err
		},
		"no owner (the legacy form)": func() error {
			_, err := openPassword(w.srv.masterKey, w.annVol.ID, nil, annSecret.PasswordEnc)
			return err
		},
	} {
		if try() == nil {
			t.Errorf("ann's ciphertext opened for %s", name)
		}
	}

	// Someone with write access to the database copies ann's ciphertext onto
	// bob's volume row: it must not connect.
	w.srv.idx.SetVolumeOwner(w.bobVol.ID, w.bob.ID, func([]byte) ([]byte, error) { return annSecret.PasswordEnc, nil })
	vol, _, _ := w.srv.idx.GetVolume(w.bobVol.ID)
	if err := w.srv.StartVolume(context.Background(), vol); err == nil || !strings.Contains(err.Error(), "decrypt") {
		t.Fatalf("a swapped ciphertext started the volume: %v", err)
	}
}

func TestVolumesCreatedThroughTheAPIBelongToTheCaller(t *testing.T) {
	w := newWorld(t)
	w.srv.newSMB = func(host, share, user, pass string) smbConnector { return newFakeSMB() }

	// The same name as ann's volume is fine for bob; it's his own namespace.
	rec := w.req(w.bobC, "POST", "/volumes", map[string]string{"name": "annvol", "host": "h", "share": "s", "username": "u", "password": "hunter2"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	var vol index.Volume
	decode(t, rec.Body.Bytes(), &vol)
	if vol.OwnerID == nil || *vol.OwnerID != w.bob.ID {
		t.Fatalf("owner = %v, want bob", vol.OwnerID)
	}
	secret, _ := w.srv.idx.GetVolumeSecret(vol.ID)
	if pw, err := openPassword(w.srv.masterKey, vol.ID, &w.bob.ID, secret.PasswordEnc); err != nil || pw != "hunter2" {
		t.Fatalf("stored password: %q, %v", pw, err)
	}
	if _, err := openPassword(w.srv.masterKey, vol.ID, nil, secret.PasswordEnc); err == nil {
		t.Error("an owned volume's password must be bound to its owner")
	}
	// Twice under one owner is a clash, and says nothing about anyone else's names.
	if rec := w.req(w.bobC, "POST", "/volumes", map[string]string{"name": "ANNVOL", "host": "h", "share": "s", "username": "u", "password": "x"}); rec.Code != http.StatusConflict {
		t.Errorf("a duplicate of your own volume's name = %d", rec.Code)
	}
	// Updating a volume re-seals with the same binding, and works.
	upd := w.req(w.bobC, "PUT", "/volumes/"+w.itoa(vol.ID), map[string]string{"name": "annvol", "host": "h2", "share": "s", "username": "u", "password": "newpass"})
	if upd.Code != http.StatusOK {
		t.Fatalf("update = %d %s", upd.Code, upd.Body)
	}
	secret, _ = w.srv.idx.GetVolumeSecret(vol.ID)
	if pw, err := openPassword(w.srv.masterKey, vol.ID, &w.bob.ID, secret.PasswordEnc); err != nil || pw != "newpass" {
		t.Fatalf("password after update: %q, %v", pw, err)
	}
}
