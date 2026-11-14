package index

import (
	"bytes"
	"errors"
	"testing"
	"time"
)

func mkUser(t *testing.T, idx *Index, name, role string) User {
	t.Helper()
	u, err := idx.CreateUser(name, role)
	if err != nil {
		t.Fatalf("CreateUser(%s): %v", name, err)
	}
	return u
}

func mkCred(t *testing.T, idx *Index, userID int64, id string) Credential {
	t.Helper()
	c, err := idx.AddCredential(Credential{UserID: userID, CredentialID: []byte(id), PublicKey: []byte("pk-" + id), Label: id})
	if err != nil {
		t.Fatalf("AddCredential(%s): %v", id, err)
	}
	return c
}

func TestUsernamesAreValidatedAndUniqueIgnoringCase(t *testing.T) {
	idx := openTest(t)
	mkUser(t, idx, "Tim", RoleAdmin)
	if _, err := idx.CreateUser("tim", RoleUser); !errors.Is(err, ErrUsernameTaken) {
		t.Fatalf("duplicate (different case) = %v, want ErrUsernameTaken", err)
	}
	for _, bad := range []string{"", " tim", "a b", "-x", "x/y", "x\x00", string(bytes.Repeat([]byte("a"), 65))} {
		if _, err := idx.CreateUser(bad, RoleUser); !errors.Is(err, ErrInvalidUser) {
			t.Errorf("CreateUser(%q) = %v, want ErrInvalidUser", bad, err)
		}
	}
	if _, err := idx.CreateUser("ok", "root"); !errors.Is(err, ErrInvalidUser) {
		t.Errorf("an unknown role = %v, want ErrInvalidUser", err)
	}
	if u, ok, _ := idx.GetUserByName("TIM"); !ok || u.Username != "Tim" {
		t.Errorf("GetUserByName is case-insensitive: %+v %v", u, ok)
	}
}

func TestCredentialsBelongToOneUserAndAreUnique(t *testing.T) {
	idx := openTest(t)
	a, b := mkUser(t, idx, "a", RoleUser), mkUser(t, idx, "b", RoleUser)
	c := mkCred(t, idx, a.ID, "cred-1")
	if _, err := idx.AddCredential(Credential{UserID: b.ID, CredentialID: []byte("cred-1"), PublicKey: []byte("x"), Label: "dup"}); !errors.Is(err, ErrCredentialUsed) {
		t.Fatalf("the same passkey for another user = %v, want ErrCredentialUsed", err)
	}
	if got, _ := idx.ListCredentials(b.ID); len(got) != 0 {
		t.Fatalf("b must not see a's passkeys: %+v", got)
	}

	// Renaming and removing are scoped to the owner.
	if ok, _ := idx.RenameCredential(b.ID, c.ID, "hijack"); ok {
		t.Fatal("b renamed a's passkey")
	}
	if ok, err := idx.DeleteCredential(b.ID, c.ID); ok || err != nil {
		t.Fatalf("b deleting a's passkey = %v, %v; want not found", ok, err)
	}
	if ok, _ := idx.RenameCredential(a.ID, c.ID, "phone"); !ok {
		t.Fatal("owner rename failed")
	}
}

func TestTheLastPasskeyCannotBeRemoved(t *testing.T) {
	idx := openTest(t)
	u := mkUser(t, idx, "u", RoleUser)
	first, second := mkCred(t, idx, u.ID, "one"), mkCred(t, idx, u.ID, "two")
	if ok, err := idx.DeleteCredential(u.ID, first.ID); !ok || err != nil {
		t.Fatalf("removing one of two = %v, %v", ok, err)
	}
	if ok, err := idx.DeleteCredential(u.ID, second.ID); !ok || !errors.Is(err, ErrLastCredential) {
		t.Fatalf("removing the last = %v, %v; want ErrLastCredential", ok, err)
	}
	if got, _ := idx.ListCredentials(u.ID); len(got) != 1 {
		t.Fatalf("the last passkey must remain: %+v", got)
	}
}

func TestRecordCredentialUseStoresCounterAndLastLogin(t *testing.T) {
	idx := openTest(t)
	u := mkUser(t, idx, "u", RoleUser)
	c := mkCred(t, idx, u.ID, "one")
	if err := idx.RecordCredentialUse(c.ID, 7, true); err != nil {
		t.Fatal(err)
	}
	got, _ := idx.ListCredentials(u.ID)
	if got[0].SignCount != 7 || !got[0].BackupState || got[0].LastUsedAt == "" {
		t.Fatalf("credential after use: %+v", got[0])
	}
	if reloaded, _, _ := idx.GetUser(u.ID); reloaded.LastLoginAt == "" {
		t.Fatal("last login not recorded")
	}
}

func TestEnrollmentsAreSingleUseExpireAndSupersede(t *testing.T) {
	idx := openTest(t)
	u := mkUser(t, idx, "u", RoleUser)
	hash := func(s string) []byte { return []byte("hash-" + s) }

	if err := idx.CreateEnrollment(u.ID, hash("a"), EnrollFirst, nil, time.Minute); err != nil {
		t.Fatal(err)
	}
	e, err := idx.LookupEnrollment(hash("a"))
	if err != nil || e.User.ID != u.ID {
		t.Fatalf("LookupEnrollment = %+v, %v", e, err)
	}
	if _, err := idx.LookupEnrollment(hash("nope")); !errors.Is(err, ErrEnrollmentInvalid) {
		t.Fatalf("unknown token = %v", err)
	}

	// A newer link replaces the older one.
	if err := idx.CreateEnrollment(u.ID, hash("b"), EnrollRecovery, nil, time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := idx.LookupEnrollment(hash("a")); !errors.Is(err, ErrEnrollmentInvalid) {
		t.Fatalf("a superseded link still works: %v", err)
	}

	// Completing spends it, atomically with storing the passkey.
	e, _ = idx.LookupEnrollment(hash("b"))
	saved, err := idx.CompleteEnrollment(e.ID, Credential{CredentialID: []byte("c1"), PublicKey: []byte("pk"), Label: "phone"})
	if err != nil || saved.UserID != u.ID {
		t.Fatalf("CompleteEnrollment = %+v, %v", saved, err)
	}
	if _, err := idx.CompleteEnrollment(e.ID, Credential{CredentialID: []byte("c2"), PublicKey: []byte("pk")}); !errors.Is(err, ErrEnrollmentInvalid) {
		t.Fatalf("a link used twice = %v, want ErrEnrollmentInvalid", err)
	}
	if got, _ := idx.ListCredentials(u.ID); len(got) != 1 {
		t.Fatalf("the second use must not store a passkey: %+v", got)
	}
	if _, err := idx.LookupEnrollment(hash("b")); !errors.Is(err, ErrEnrollmentInvalid) {
		t.Fatalf("a used link still looks up: %v", err)
	}

	// Expired.
	if err := idx.CreateEnrollment(u.ID, hash("old"), EnrollFirst, nil, -time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := idx.LookupEnrollment(hash("old")); !errors.Is(err, ErrEnrollmentInvalid) {
		t.Fatalf("an expired link = %v", err)
	}
	// A duplicate credential id doesn't burn the link.
	if err := idx.CreateEnrollment(u.ID, hash("dup"), EnrollRecovery, nil, time.Minute); err != nil {
		t.Fatal(err)
	}
	e, _ = idx.LookupEnrollment(hash("dup"))
	if _, err := idx.CompleteEnrollment(e.ID, Credential{CredentialID: []byte("c1"), PublicKey: []byte("pk")}); !errors.Is(err, ErrCredentialUsed) {
		t.Fatalf("registering an existing passkey = %v, want ErrCredentialUsed", err)
	}
	if _, err := idx.LookupEnrollment(hash("dup")); err != nil {
		t.Fatalf("a failed registration burned the link: %v", err)
	}

	// A disabled user's link is refused.
	disabled := true
	if _, err := idx.UpdateUser(u.ID, nil, &disabled); err != nil {
		t.Fatal(err)
	}
	if _, err := idx.LookupEnrollment(hash("dup")); !errors.Is(err, ErrEnrollmentInvalid) {
		t.Fatalf("a disabled user's link works: %v", err)
	}
}

func TestSessionsResolveExpireAndSlide(t *testing.T) {
	idx := openTest(t)
	u := mkUser(t, idx, "u", RoleUser)
	h := func(s string) []byte { return []byte("tok-" + s) }

	if _, err := idx.CreateSession(u.ID, nil, h("live"), SessionTTL, "UA", "1.2.3.4"); err != nil {
		t.Fatal(err)
	}
	su, ok, err := idx.SessionByToken(h("live"))
	if err != nil || !ok || su.User.ID != u.ID || su.Refreshed {
		t.Fatalf("SessionByToken = %+v %v %v", su, ok, err)
	}
	if _, ok, _ := idx.SessionByToken(h("unknown")); ok {
		t.Fatal("an unknown token resolved")
	}

	// Expired sessions don't resolve (a negative ttl makes one).
	if _, err := idx.CreateSession(u.ID, nil, h("dead"), -time.Minute, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := idx.SessionByToken(h("dead")); ok {
		t.Fatal("an expired session resolved")
	}

	// A session idle for a while is pushed out again on use.
	if _, err := idx.db.Exec(`UPDATE sessions SET last_used_at = now() - interval '1 hour', expires_at = now() + interval '1 day' WHERE token_hash = $1`, h("live")); err != nil {
		t.Fatal(err)
	}
	su, ok, _ = idx.SessionByToken(h("live"))
	if !ok || !su.Refreshed {
		t.Fatalf("an idle session should refresh: %+v %v", su, ok)
	}
	var days float64
	if err := idx.db.QueryRow(`SELECT extract(epoch FROM expires_at - now()) / 86400 FROM sessions WHERE token_hash = $1`, h("live")).Scan(&days); err != nil || days < 29 {
		t.Fatalf("expiry after use = %.1f days (%v), want about 30", days, err)
	}
}

func TestDisablingAUserEndsTheirSessionsAndBlocksNewOnes(t *testing.T) {
	idx := openTest(t)
	u := mkUser(t, idx, "u", RoleUser)
	idx.CreateSession(u.ID, nil, []byte("t1"), SessionTTL, "", "")
	idx.CreateSession(u.ID, nil, []byte("t2"), SessionTTL, "", "")

	yes := true
	got, err := idx.UpdateUser(u.ID, nil, &yes)
	if err != nil || !got.Disabled {
		t.Fatalf("UpdateUser = %+v, %v", got, err)
	}
	for _, tok := range []string{"t1", "t2"} {
		if _, ok, _ := idx.SessionByToken([]byte(tok)); ok {
			t.Errorf("session %s survived disabling its user", tok)
		}
	}
	if n, _ := idx.ListSessions(u.ID); len(n) != 0 {
		t.Errorf("sessions remain: %+v", n)
	}
	// Even a session row that somehow exists doesn't resolve for a disabled user.
	idx.CreateSession(u.ID, nil, []byte("t3"), SessionTTL, "", "")
	if _, ok, _ := idx.SessionByToken([]byte("t3")); ok {
		t.Error("a disabled user's session resolved")
	}
}

func TestRemovingAPasskeySignsOutWhatItSignedIn(t *testing.T) {
	idx := openTest(t)
	u := mkUser(t, idx, "u", RoleUser)
	phone, laptop := mkCred(t, idx, u.ID, "phone"), mkCred(t, idx, u.ID, "laptop")
	idx.CreateSession(u.ID, &phone.ID, []byte("from-phone"), SessionTTL, "", "")
	idx.CreateSession(u.ID, &laptop.ID, []byte("from-laptop"), SessionTTL, "", "")

	if ok, err := idx.DeleteCredential(u.ID, phone.ID); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if _, ok, _ := idx.SessionByToken([]byte("from-phone")); ok {
		t.Error("the removed passkey's session is still valid")
	}
	if _, ok, _ := idx.SessionByToken([]byte("from-laptop")); !ok {
		t.Error("the other passkey's session was ended")
	}
}

func TestSessionsAreListedAndEndedOnlyByTheirOwner(t *testing.T) {
	idx := openTest(t)
	a, b := mkUser(t, idx, "a", RoleUser), mkUser(t, idx, "b", RoleUser)
	sid, _ := idx.CreateSession(a.ID, nil, []byte("ta"), SessionTTL, "UA", "ip")
	idx.CreateSession(b.ID, nil, []byte("tb"), SessionTTL, "UA", "ip")

	if got, _ := idx.ListSessions(a.ID); len(got) != 1 || got[0].ID != sid {
		t.Fatalf("a's sessions: %+v", got)
	}
	if ok, _ := idx.DeleteSession(b.ID, sid); ok {
		t.Fatal("b ended a's session")
	}
	if ok, _ := idx.DeleteSession(a.ID, sid); !ok {
		t.Fatal("a could not end their own session")
	}
	if _, ok, _ := idx.SessionByToken([]byte("ta")); ok {
		t.Fatal("an ended session still resolves")
	}
}

func TestDeletingAUserRemovesTheirAccountData(t *testing.T) {
	idx := openTest(t)
	u := mkUser(t, idx, "u", RoleUser)
	c := mkCred(t, idx, u.ID, "c")
	idx.CreateSession(u.ID, &c.ID, []byte("t"), SessionTTL, "", "")
	idx.CreateEnrollment(u.ID, []byte("e"), EnrollFirst, nil, time.Minute)

	if err := idx.DeleteUser(u.ID); err != nil {
		t.Fatal(err)
	}
	for table, q := range map[string]string{
		"credentials": `SELECT count(*) FROM credentials`, "sessions": `SELECT count(*) FROM sessions`, "enrollments": `SELECT count(*) FROM enrollments`,
	} {
		var n int
		idx.db.QueryRow(q).Scan(&n)
		if n != 0 {
			t.Errorf("%s left %d rows after deleting the user", table, n)
		}
	}
	if err := idx.DeleteUser(u.ID); !errors.Is(err, ErrUserNotFound) {
		t.Errorf("deleting twice = %v", err)
	}
}

func TestAUserWithVolumesCannotBeDeletedUnderThem(t *testing.T) {
	idx := openTest(t)
	u := mkUser(t, idx, "u", RoleUser)
	if _, err := idx.CreateVolume(u.ID, "v", "h", "s", "x", sealed([]byte("x"))); err != nil {
		t.Fatal(err)
	}
	if err := idx.DeleteUser(u.ID); err == nil {
		t.Fatal("deleting a user who owns a volume must fail, not orphan it")
	}
}

func TestVolumesBelongToOneOwnerAndNamesAreUniquePerOwner(t *testing.T) {
	idx := openTest(t)
	a, b := mkUser(t, idx, "a", RoleUser), mkUser(t, idx, "b", RoleUser)

	var sealedFor int64
	va, err := idx.CreateVolume(a.ID, "NAS", "h", "s", "u", func(id int64) ([]byte, error) { sealedFor = id; return []byte("pw"), nil })
	if err != nil {
		t.Fatal(err)
	}
	if sealedFor != va.ID || va.OwnerID == nil || *va.OwnerID != a.ID {
		t.Fatalf("seal got id %d for volume %d, owner %v", sealedFor, va.ID, va.OwnerID)
	}
	secret, _ := idx.GetVolumeSecret(va.ID)
	if string(secret.PasswordEnc) != "pw" || secret.ID != va.ID || secret.OwnerID == nil || *secret.OwnerID != a.ID {
		t.Fatalf("secret = %+v", secret)
	}

	if _, err := idx.CreateVolume(a.ID, "nas", "h", "s", "u", sealed([]byte("x"))); !errors.Is(err, ErrVolumeNameTaken) {
		t.Fatalf("same name, same owner = %v, want ErrVolumeNameTaken", err)
	}
	vb, err := idx.CreateVolume(b.ID, "nas", "h", "s", "u", sealed([]byte("x")))
	if err != nil {
		t.Fatalf("another owner may reuse the name: %v", err)
	}

	if ids, _ := idx.VolumeIDsByOwner(a.ID); len(ids) != 1 || ids[0] != va.ID {
		t.Errorf("a's scope = %v", ids)
	}
	if vols, _ := idx.ListVolumesByOwner(b.ID); len(vols) != 1 || vols[0].ID != vb.ID {
		t.Errorf("b's volumes = %+v", vols)
	}
	if ids, _ := idx.VolumeIDsByOwner(9999); ids == nil || len(ids) != 0 {
		t.Errorf("a stranger's scope must be empty, not nil: %#v", ids)
	}

	// A failed seal leaves no half-created volume behind.
	if _, err := idx.CreateVolume(a.ID, "never", "h", "s", "u", func(int64) ([]byte, error) { return nil, errors.New("boom") }); err == nil {
		t.Fatal("seal error must fail the create")
	}
	if all, _ := idx.ListVolumes(); len(all) != 2 {
		t.Fatalf("volumes after a failed create: %+v", all)
	}
}

func TestChangingAnOwnerReseals(t *testing.T) {
	idx := openTest(t)
	a, b := mkUser(t, idx, "a", RoleUser), mkUser(t, idx, "b", RoleUser)
	v, _ := idx.CreateVolume(a.ID, "NAS", "h", "s", "u", sealed([]byte("old")))
	other, _ := idx.CreateVolume(b.ID, "NAS", "h2", "s", "u", sealed([]byte("x")))

	if _, err := idx.SetVolumeOwner(v.ID, b.ID, func([]byte) ([]byte, error) { return []byte("new"), nil }); !errors.Is(err, ErrVolumeNameTaken) {
		t.Fatalf("moving onto an owner with that name = %v, want ErrVolumeNameTaken", err)
	}
	if secret, _ := idx.GetVolumeSecret(v.ID); string(secret.PasswordEnc) != "old" || *secret.OwnerID != a.ID {
		t.Fatalf("a refused move changed the volume: %+v", secret)
	}

	idx.UpdateVolume(other.ID, VolumeUpdate{Name: "Other", Host: "h2", Share: "s", Username: "u", Enabled: true})
	got, err := idx.SetVolumeOwner(v.ID, b.ID, func(old []byte) ([]byte, error) {
		if string(old) != "old" {
			t.Errorf("reseal got %q", old)
		}
		return []byte("new"), nil
	})
	if err != nil || *got.OwnerID != b.ID {
		t.Fatalf("SetVolumeOwner = %+v, %v", got, err)
	}
	if secret, _ := idx.GetVolumeSecret(v.ID); string(secret.PasswordEnc) != "new" {
		t.Fatalf("password not resealed: %q", secret.PasswordEnc)
	}
}

func TestVolumeAADBindsIdAndOwner(t *testing.T) {
	one, two := int64(1), int64(2)
	if VolumeAAD(5, nil) != nil {
		t.Error("an unowned volume has no AAD")
	}
	if bytes.Equal(VolumeAAD(5, &one), VolumeAAD(5, &two)) || bytes.Equal(VolumeAAD(5, &one), VolumeAAD(6, &one)) {
		t.Error("AAD must differ by owner and by volume")
	}
}

func TestAdoptUnownedVolumes(t *testing.T) {
	idx := openTest(t)
	// Volumes from before accounts: no owner.
	for _, name := range []string{"one", "two"} {
		if _, err := idx.db.Exec(`INSERT INTO volumes (name, host, share, username, password_enc, created_at, updated_at)
			VALUES ($1, 'h', 's', 'u', 'legacy', now(), now())`, name); err != nil {
			t.Fatal(err)
		}
	}
	admin := mkUser(t, idx, "admin", RoleAdmin)
	other, _ := idx.CreateVolume(admin.ID, "mine", "h", "s", "u", sealed([]byte("kept")))

	n, err := idx.AdoptUnownedVolumes(admin.ID, func(id int64, enc []byte) ([]byte, error) {
		if string(enc) != "legacy" {
			t.Errorf("reseal of %d got %q", id, enc)
		}
		return []byte("sealed-for-owner"), nil
	})
	if err != nil || n != 2 {
		t.Fatalf("AdoptUnownedVolumes = %d, %v", n, err)
	}
	ids, _ := idx.VolumeIDsByOwner(admin.ID)
	if len(ids) != 3 {
		t.Fatalf("admin owns %v", ids)
	}
	if secret, _ := idx.GetVolumeSecret(other.ID); string(secret.PasswordEnc) != "kept" {
		t.Error("an already-owned volume must not be touched")
	}
	if n, _ := idx.AdoptUnownedVolumes(admin.ID, func(int64, []byte) ([]byte, error) { t.Error("nothing left to adopt"); return nil, nil }); n != 0 {
		t.Errorf("second adoption = %d", n)
	}

	// A failing reseal adopts nothing.
	idx.db.Exec(`INSERT INTO volumes (name, host, share, username, password_enc, created_at, updated_at) VALUES ('three', 'h', 's', 'u', 'legacy', now(), now())`)
	if _, err := idx.AdoptUnownedVolumes(admin.ID, func(int64, []byte) ([]byte, error) { return nil, errors.New("bad key") }); err == nil {
		t.Fatal("a failing reseal must fail the adoption")
	}
	if ids, _ := idx.VolumeIDsByOwner(admin.ID); len(ids) != 3 {
		t.Fatalf("a failed adoption changed ownership: %v", ids)
	}
}

func TestQueriesAreLimitedToTheScopeGiven(t *testing.T) {
	idx := openTest(t)
	seedFiles(t, idx, 1, File{Path: "mine.txt", Size: 1, ModTime: 1})
	seedFiles(t, idx, 2, File{Path: "theirs.txt", Size: 1, ModTime: 1})

	if got, _ := idx.Search([]int64{1}, "txt", 10, true, TimeRange{}); len(got) != 1 || got[0].Path != "mine.txt" {
		t.Errorf("scope {1}: %+v", got)
	}
	for _, empty := range [][]int64{nil, {}} {
		if got, err := idx.Search(empty, "txt", 10, true, TimeRange{}); err != nil || len(got) != 0 {
			t.Errorf("an empty scope must find nothing, got %+v %v", got, err)
		}
		if got, err := idx.SearchContent(empty, "anything", 10, true, TimeRange{}); err != nil || len(got) != 0 {
			t.Errorf("an empty scope must find no content, got %+v %v", got, err)
		}
		if st, _ := idx.Stats(empty); st.FileCount != 0 {
			t.Errorf("an empty scope counts nothing, got %+v", st)
		}
		if p, err := idx.ContentProgress(empty, textExts, 1000); err != nil || len(p) != 0 {
			t.Errorf("an empty scope reports no progress, got %+v %v", p, err)
		}
	}
	if st, _ := idx.Stats([]int64{2}); st.FileCount != 1 {
		t.Errorf("Stats({2}) = %+v", st)
	}
}
