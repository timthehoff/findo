package auth

import (
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

	id := cs.Put(Challenge{Kind: KindLogin})
	if c, ok := cs.Take(id); !ok || c.Kind != KindLogin {
		t.Fatalf("Take = %+v %v", c, ok)
	}
	if _, ok := cs.Take(id); ok {
		t.Fatal("a challenge must work once")
	}
	if _, ok := cs.Take("nope"); ok {
		t.Fatal("an unknown challenge resolved")
	}

	id = cs.Put(Challenge{Kind: KindLogin})
	now = now.Add(ChallengeTTL + time.Second)
	if _, ok := cs.Take(id); ok {
		t.Fatal("an expired challenge resolved")
	}
}

func TestChallengeStoreIsBounded(t *testing.T) {
	cs := NewChallenges()
	first := cs.Put(Challenge{Kind: KindLogin})
	for i := 0; i < maxChallenges+10; i++ {
		cs.Put(Challenge{Kind: KindLogin})
	}
	if len(cs.m) > maxChallenges {
		t.Fatalf("store grew to %d", len(cs.m))
	}
	if _, ok := cs.Take(first); ok {
		t.Fatal("the oldest challenge should have been dropped first")
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
