package index

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func mkInvite(t *testing.T, idx *Index, label string, ttl time.Duration, uses int) (Invite, []byte) {
	t.Helper()
	admin, ok, _ := idx.GetUserByName("admin")
	if !ok {
		admin = mkUser(t, idx, "admin", RoleAdmin)
	}
	hash := []byte("tok-" + label)
	v, err := idx.CreateInvite(label, hash, admin.ID, ttl, uses)
	if err != nil {
		t.Fatalf("CreateInvite(%s): %v", label, err)
	}
	return v, hash
}

func join(t *testing.T, idx *Index, inviteID int64, username, credID string) (User, error) {
	t.Helper()
	id, err := idx.ReserveUserID()
	if err != nil {
		t.Fatal(err)
	}
	u, _, err := idx.CompleteInvite(inviteID, id, username, Credential{CredentialID: []byte(credID), PublicKey: []byte("pk"), Label: "d"})
	return u, err
}

func TestAnInviteLooksUpWhileItIsUsable(t *testing.T) {
	idx := openTest(t)
	v, hash := mkInvite(t, idx, "family", time.Hour, 2)
	if v.Status != InviteActive || v.MaxUses != 2 || v.Uses != 0 || v.CreatedByName != "admin" {
		t.Fatalf("created: %+v", v)
	}
	if got, err := idx.LookupInvite(hash); err != nil || got.ID != v.ID {
		t.Fatalf("LookupInvite = %+v, %v", got, err)
	}
	if _, err := idx.LookupInvite([]byte("nope")); !errors.Is(err, ErrInviteInvalid) {
		t.Fatalf("unknown token = %v", err)
	}

	// Expired.
	_, oldHash := mkInvite(t, idx, "old", -time.Minute, 1)
	if _, err := idx.LookupInvite(oldHash); !errors.Is(err, ErrInviteInvalid) {
		t.Fatalf("an expired invite = %v", err)
	}
	// Revoked.
	rv, revHash := mkInvite(t, idx, "revoked", time.Hour, 1)
	if ok, _ := idx.RevokeInvite(rv.ID); !ok {
		t.Fatal("revoke")
	}
	if _, err := idx.LookupInvite(revHash); !errors.Is(err, ErrInviteInvalid) {
		t.Fatalf("a revoked invite = %v", err)
	}
	if ok, _ := idx.RevokeInvite(99999); ok {
		t.Fatal("revoking nothing reported success")
	}
}

func TestJoiningMakesAnOrdinaryUserWithTheirPasskeyAndCountsAUse(t *testing.T) {
	idx := openTest(t)
	v, _ := mkInvite(t, idx, "family", time.Hour, 2)

	id, _ := idx.ReserveUserID()
	user, cred, err := idx.CompleteInvite(v.ID, id, "Ann", Credential{CredentialID: []byte("c1"), PublicKey: []byte("pk"), Label: "phone"})
	if err != nil {
		t.Fatal(err)
	}
	if user.ID != id || user.Username != "Ann" || user.Role != RoleUser || user.Disabled {
		t.Fatalf("user: %+v", user)
	}
	if cred.UserID != id || cred.Label != "phone" {
		t.Fatalf("credential: %+v", cred)
	}
	creds, _ := idx.ListCredentials(id)
	if len(creds) != 1 {
		t.Fatalf("passkeys: %+v", creds)
	}
	invites, _ := idx.ListInvites()
	if invites[0].Uses != 1 || invites[0].Status != InviteActive {
		t.Fatalf("after one of two uses: %+v", invites[0])
	}
	users, _ := idx.ListUsers()
	var found bool
	for _, u := range users {
		if u.Username == "Ann" {
			found = u.InviteLabel == "family"
		}
	}
	if !found {
		t.Fatalf("the user should record the invite they came from: %+v", users)
	}

	// Last use, then it's spent.
	if _, err := join(t, idx, v.ID, "bob", "c2"); err != nil {
		t.Fatal(err)
	}
	if _, err := join(t, idx, v.ID, "carl", "c3"); !errors.Is(err, ErrInviteInvalid) {
		t.Fatalf("a third use of a two-use invite = %v", err)
	}
	if invites, _ = idx.ListInvites(); invites[0].Status != InviteUsedUp || invites[0].Uses != 2 {
		t.Fatalf("used up: %+v", invites[0])
	}
	if _, ok, _ := idx.GetUserByName("carl"); ok {
		t.Fatal("a refused join created the account")
	}
}

func TestAFailedJoinDoesNotSpendAUse(t *testing.T) {
	idx := openTest(t)
	v, _ := mkInvite(t, idx, "one", time.Hour, 1)
	mkUser(t, idx, "taken", RoleUser)
	mkCred(t, idx, 1, "existing")

	if _, err := join(t, idx, v.ID, "TAKEN", "c1"); !errors.Is(err, ErrUsernameTaken) {
		t.Fatalf("a name that's taken (any case) = %v", err)
	}
	if _, err := join(t, idx, v.ID, "fresh", "existing"); !errors.Is(err, ErrCredentialUsed) {
		t.Fatalf("a passkey that's already registered = %v", err)
	}
	if _, ok, _ := idx.GetUserByName("fresh"); ok {
		t.Fatal("a join that failed at the passkey left its account behind")
	}
	id, _ := idx.ReserveUserID()
	if _, _, err := idx.CompleteInvite(v.ID, id, "no good", Credential{CredentialID: []byte("x"), PublicKey: []byte("k")}); !errors.Is(err, ErrInvalidUser) {
		t.Fatalf("an invalid name = %v", err)
	}
	if invites, _ := idx.ListInvites(); invites[0].Uses != 0 {
		t.Fatalf("failures spent uses: %+v", invites[0])
	}
	// The single use is still there.
	if _, err := join(t, idx, v.ID, "fresh", "c-ok"); err != nil {
		t.Fatalf("the use should still be available: %v", err)
	}
}

func TestInviteJoinsTakeTurnsForTheLastUse(t *testing.T) {
	for round := 0; round < 5; round++ {
		idx := openTest(t)
		v, _ := mkInvite(t, idx, "last", time.Hour, 1)

		var wg sync.WaitGroup
		results := make(chan error, 6)
		for i := 0; i < 6; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				id, err := idx.ReserveUserID()
				if err != nil {
					results <- err
					return
				}
				_, _, err = idx.CompleteInvite(v.ID, id, "user"+string(rune('a'+i)), Credential{CredentialID: []byte{byte(i)}, PublicKey: []byte("k")})
				results <- err
			}(i)
		}
		wg.Wait()
		close(results)
		won := 0
		for err := range results {
			if err == nil {
				won++
			} else if !errors.Is(err, ErrInviteInvalid) {
				t.Fatalf("round %d: %v", round, err)
			}
		}
		if won != 1 {
			t.Fatalf("round %d: %d joins succeeded on a one-use invite", round, won)
		}
		if users, _ := idx.ListUsers(); len(users) != 2 { // the admin and the one joiner
			t.Fatalf("round %d: %d users", round, len(users))
		}
	}
}

func TestReservedIDsAreUniqueAndUsable(t *testing.T) {
	idx := openTest(t)
	a, _ := idx.ReserveUserID()
	b, _ := idx.ReserveUserID()
	if a == b {
		t.Fatal("reserved the same id twice")
	}
	// A reservation nobody uses leaves the next created user a fresh id.
	u := mkUser(t, idx, "later", RoleUser)
	if u.ID == a || u.ID == b {
		t.Fatalf("a created user reused a reserved id: %d", u.ID)
	}
}

func TestRevokingStopsJoinsButKeepsExistingAccounts(t *testing.T) {
	idx := openTest(t)
	v, _ := mkInvite(t, idx, "x", time.Hour, 5)
	user, err := join(t, idx, v.ID, "early", "c1")
	if err != nil {
		t.Fatal(err)
	}
	idx.RevokeInvite(v.ID)
	if _, err := join(t, idx, v.ID, "late", "c2"); !errors.Is(err, ErrInviteInvalid) {
		t.Fatalf("a join after revoking = %v", err)
	}
	if got, ok, _ := idx.GetUser(user.ID); !ok || got.Disabled {
		t.Fatal("revoking an invite must leave the accounts it made")
	}
	if invites, _ := idx.ListInvites(); invites[0].Status != InviteRevoked {
		t.Fatalf("status: %+v", invites[0])
	}
}

func TestOldInvitesArePrunedAndLeaveTheirUsersBehind(t *testing.T) {
	idx := openTest(t)
	v, _ := mkInvite(t, idx, "ancient", time.Hour, 1)
	user, _ := join(t, idx, v.ID, "joiner", "c1")
	if _, err := idx.db.Exec(`UPDATE invites SET expires_at = now() - interval '40 days'`); err != nil {
		t.Fatal(err)
	}
	mkInvite(t, idx, "new", time.Hour, 1) // creating one prunes the old
	invites, _ := idx.ListInvites()
	if len(invites) != 1 || invites[0].Label != "new" {
		t.Fatalf("invites: %+v", invites)
	}
	if _, ok, _ := idx.GetUser(user.ID); !ok {
		t.Fatal("pruning an invite removed an account")
	}
}

func TestDeletingTheCreatorKeepsTheirInvites(t *testing.T) {
	idx := openTest(t)
	creator := mkUser(t, idx, "creator", RoleAdmin)
	if _, err := idx.CreateInvite("kept", []byte("h"), creator.ID, time.Hour, 1); err != nil {
		t.Fatal(err)
	}
	if err := idx.DeleteUser(creator.ID); err != nil {
		t.Fatal(err)
	}
	invites, _ := idx.ListInvites()
	if len(invites) != 1 || invites[0].CreatedByName != "" || invites[0].Status != InviteActive {
		t.Fatalf("invites after the creator was deleted: %+v", invites)
	}
}

func TestInviteLimitsAreEnforcedByTheSchema(t *testing.T) {
	idx := openTest(t)
	admin := mkUser(t, idx, "admin", RoleAdmin)
	for _, n := range []int{0, 101, -1} {
		if _, err := idx.CreateInvite("x", []byte{byte(n)}, admin.ID, time.Hour, n); err == nil {
			t.Errorf("max_uses %d was accepted", n)
		}
	}
}
