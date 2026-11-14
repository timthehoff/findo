package auth

import (
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
)

// Challenge kinds.
const (
	KindLogin    = "login"    // discoverable sign-in
	KindEnroll   = "enroll"   // registering via an enrollment link
	KindRegister = "register" // a signed-in user adding a passkey
	KindReauth   = "reauth"   // a signed-in user confirming with a passkey
)

// ChallengeTTL is how long a begun ceremony stays valid.
const ChallengeTTL = 5 * time.Minute

// Bounds on the store. login/begin is open to anyone, so without limits a
// client could grow it without end, or push other people's sign-ins out of it.
// Each owner (an address, or a signed-in user) may hold maxPerOwner begun
// ceremonies and evicts only its own oldest beyond that; the store as a whole
// refuses new ones when full rather than evicting anyone else's.
const (
	maxPerOwner   = 10
	maxChallenges = 5000
)

// Challenge is a begun WebAuthn ceremony waiting for its finish call.
type Challenge struct {
	Kind         string
	Session      webauthn.SessionData
	UserID       int64  // KindRegister, KindReauth: who it's for
	EnrollmentID int64  // KindEnroll
	Label        string // the passkey's name, for the two registration kinds
	owner        string
	expires      time.Time
}

// Challenges is a short-lived, single-use store of begun ceremonies, in memory
// only: losing it on restart just means a sign-in is retried.
type Challenges struct {
	mu  sync.Mutex
	m   map[string]Challenge
	now func() time.Time
}

func NewChallenges() *Challenges {
	return &Challenges{m: map[string]Challenge{}, now: time.Now}
}

// Put stores c for owner and returns its id; ok is false when the store is
// full of other people's live ceremonies.
func (cs *Challenges) Put(owner string, c Challenge) (id string, ok bool) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	now := cs.now()
	c.owner, c.expires = owner, now.Add(ChallengeTTL)

	// Over its own allowance: drop this owner's oldest, nobody else's.
	for {
		count, oldest := 0, ""
		var oldestAt time.Time
		for k, v := range cs.m {
			if v.owner != owner {
				continue
			}
			count++
			if oldest == "" || v.expires.Before(oldestAt) {
				oldest, oldestAt = k, v.expires
			}
		}
		if count < maxPerOwner {
			break
		}
		delete(cs.m, oldest)
	}

	if len(cs.m) >= maxChallenges {
		for k, v := range cs.m {
			if now.After(v.expires) {
				delete(cs.m, k)
			}
		}
		if len(cs.m) >= maxChallenges {
			return "", false
		}
	}
	id, _ = NewToken()
	cs.m[id] = c
	return id, true
}

// Take removes and returns the challenge for id: each can be finished once.
func (cs *Challenges) Take(id string) (Challenge, bool) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	c, ok := cs.m[id]
	delete(cs.m, id)
	if !ok || cs.now().After(c.expires) {
		return Challenge{}, false
	}
	return c, true
}
