package auth

import (
	"strconv"
	"testing"
	"time"

	"github.com/thoff/findo/backend/internal/index"
)

func TestTokensAreRandomAndOnlyTheirHashIsStored(t *testing.T) {
	a, ha := NewToken()
	b, _ := NewToken()
	if a == b || len(a) < 40 {
		t.Fatalf("tokens must be long and distinct: %q %q", a, b)
	}
	if string(HashToken(a)) != string(ha) || string(HashToken(b)) == string(ha) {
		t.Fatal("HashToken must be deterministic per token")
	}
	if len(ha) != 32 {
		t.Fatalf("hash length = %d", len(ha))
	}
}

func TestChallengesAreSingleUseAndExpire(t *testing.T) {
	now := time.Unix(1000, 0)
	cs := NewChallenges()
	cs.now = func() time.Time { return now }

	id, _ := cs.Put("a", Challenge{Kind: KindLogin})
	if c, ok := cs.Take(id); !ok || c.Kind != KindLogin {
		t.Fatalf("Take = %+v %v", c, ok)
	}
	if _, ok := cs.Take(id); ok {
		t.Fatal("a challenge must work once")
	}
	if _, ok := cs.Take("nope"); ok {
		t.Fatal("an unknown challenge resolved")
	}

	id, _ = cs.Put("a", Challenge{Kind: KindLogin})
	now = now.Add(ChallengeTTL + time.Second)
	if _, ok := cs.Take(id); ok {
		t.Fatal("an expired challenge resolved")
	}
}

// One client flooding begin must only ever push out its own ceremonies.
func TestAnOwnerCanOnlyEvictItsOwnChallenges(t *testing.T) {
	cs := NewChallenges()
	victim, _ := cs.Put("victim", Challenge{Kind: KindLogin})
	first, _ := cs.Put("attacker", Challenge{Kind: KindLogin})
	for i := 0; i < 500; i++ {
		cs.Put("attacker", Challenge{Kind: KindLogin})
	}
	if _, ok := cs.Take(victim); !ok {
		t.Fatal("another owner's flood evicted the victim's challenge")
	}
	if _, ok := cs.Take(first); ok {
		t.Fatal("the flooder's own oldest should have been dropped")
	}
	n := 0
	for _, c := range cs.m {
		if c.owner == "attacker" {
			n++
		}
	}
	if n != maxPerOwner {
		t.Fatalf("attacker holds %d, want the cap of %d", n, maxPerOwner)
	}
}

// With every slot taken by live ceremonies, new ones are refused rather than
// evicting someone else's; expired ones free their slots.
func TestAFullStoreRefusesNewChallengesInsteadOfEvicting(t *testing.T) {
	now := time.Unix(1000, 0)
	cs := NewChallenges()
	cs.now = func() time.Time { return now }
	var first string
	for i := 0; i < maxChallenges; i++ {
		id, ok := cs.Put("owner-"+strconv.Itoa(i), Challenge{Kind: KindLogin})
		if !ok {
			t.Fatalf("refused %d before the store was full", i)
		}
		if i == 0 {
			first = id
		}
	}
	if _, ok := cs.Put("newcomer", Challenge{Kind: KindLogin}); ok {
		t.Fatal("a full store of live challenges accepted another")
	}
	if _, ok := cs.Take(first); !ok {
		t.Fatal("refusing a newcomer must not evict anyone")
	}
	now = now.Add(ChallengeTTL + time.Second)
	if _, ok := cs.Put("newcomer", Challenge{Kind: KindLogin}); !ok {
		t.Fatal("expired challenges should free their slots")
	}
}

func TestLimiterBacksOffAfterFreeFailuresAndResets(t *testing.T) {
	now := time.Unix(1000, 0)
	l := NewLimiter()
	l.now = func() time.Time { return now }
	const ip = "203.0.113.9"

	for i := 0; i < freeFailures; i++ {
		if ok, _ := l.Allow(ip); !ok {
			t.Fatalf("blocked after only %d failures", i)
		}
		l.Fail(ip)
	}
	if ok, _ := l.Allow(ip); !ok {
		t.Fatal("the free failures should not block")
	}
	l.Fail(ip) // first one over: wait baseDelay
	ok, wait := l.Allow(ip)
	if ok || wait != baseDelay {
		t.Fatalf("after the 6th failure: ok=%v wait=%v, want blocked for %v", ok, wait, baseDelay)
	}
	now = now.Add(baseDelay)
	if ok, _ := l.Allow(ip); !ok {
		t.Fatal("should be allowed again once the delay passes")
	}
	l.Fail(ip) // doubles
	if _, wait := l.Allow(ip); wait != 2*baseDelay {
		t.Fatalf("second delay = %v, want %v", wait, 2*baseDelay)
	}
	for i := 0; i < 40; i++ {
		now = now.Add(maxDelay)
		l.Fail(ip)
	}
	if _, wait := l.Allow(ip); wait > maxDelay {
		t.Fatalf("delay %v exceeds the cap %v", wait, maxDelay)
	}

	// Other addresses are unaffected, and success clears the record.
	if ok, _ := l.Allow("198.51.100.1"); !ok {
		t.Fatal("another address was blocked")
	}
	l.Succeed(ip)
	if ok, _ := l.Allow(ip); !ok {
		t.Fatal("success should clear the backoff")
	}
}

func TestLimiterForgetsIdleAddresses(t *testing.T) {
	now := time.Unix(1000, 0)
	l := NewLimiter()
	l.now = func() time.Time { return now }
	for i := 0; i < 20; i++ {
		l.Fail("x")
	}
	if ok, _ := l.Allow("x"); ok {
		t.Fatal("should be blocked")
	}
	now = now.Add(forgetAfter + time.Minute)
	if ok, _ := l.Allow("x"); !ok {
		t.Fatal("an idle address should be forgiven")
	}
}

func TestUserHandlesRoundTrip(t *testing.T) {
	for _, id := range []int64{1, 42, 1 << 40} {
		got, ok := UserIDFromHandle(Handle(id))
		if !ok || got != id {
			t.Errorf("handle for %d decoded to %d, %v", id, got, ok)
		}
	}
	for _, bad := range [][]byte{nil, {1, 2, 3}, make([]byte, 9), {0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}} {
		if id, ok := UserIDFromHandle(bad); ok {
			t.Errorf("handle %v decoded to %d", bad, id)
		}
	}
}

func TestCredentialConversionKeepsTheFlagsTheLibraryChecks(t *testing.T) {
	c := index.Credential{CredentialID: []byte("id"), PublicKey: []byte("pk"), SignCount: 9, Transports: []string{"internal"}, BackupEligible: true, BackupState: true}
	w := ToWebAuthn(c)
	if !w.Flags.BackupEligible || !w.Flags.BackupState || !w.Flags.UserVerified || w.Authenticator.SignCount != 9 || len(w.Transport) != 1 {
		t.Fatalf("converted credential: %+v", w)
	}
	back := FromWebAuthn(7, &w, "phone")
	if back.UserID != 7 || back.Label != "phone" || !back.BackupEligible || back.SignCount != 9 || back.Transports[0] != "internal" {
		t.Fatalf("round trip: %+v", back)
	}
}

func TestRateAllowsNPerWindowPerKey(t *testing.T) {
	now := time.Unix(1000, 0)
	r := NewRate(3, time.Minute)
	r.now = func() time.Time { return now }

	for i := 0; i < 3; i++ {
		if ok, _ := r.Allow("a"); !ok {
			t.Fatalf("event %d refused within the allowance", i+1)
		}
	}
	now = now.Add(20 * time.Second)
	ok, wait := r.Allow("a")
	if ok || wait != 40*time.Second {
		t.Fatalf("4th event: ok=%v wait=%v, want refused for the 40s left", ok, wait)
	}
	if ok, _ := r.Allow("b"); !ok {
		t.Fatal("another key shares no budget")
	}
	now = now.Add(41 * time.Second)
	if ok, _ := r.Allow("a"); !ok {
		t.Fatal("a new window should start fresh")
	}
}
