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
)

// ChallengeTTL is how long a begun ceremony stays valid.
const ChallengeTTL = 5 * time.Minute

// maxChallenges bounds memory: login/begin is open to anyone, so without a cap
// a client could grow the store without limit.
const maxChallenges = 2000

// Challenge is a begun WebAuthn ceremony waiting for its finish call.
type Challenge struct {
	Kind         string
	Session      webauthn.SessionData
	UserID       int64  // KindRegister: who is adding a passkey
	EnrollmentID int64  // KindEnroll
	Label        string // the passkey's name, for the two registration kinds
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

// Put stores c and returns its id.
func (cs *Challenges) Put(c Challenge) string {
	id, _ := NewToken()
	cs.mu.Lock()
	defer cs.mu.Unlock()
	now := cs.now()
	c.expires = now.Add(ChallengeTTL)
	if len(cs.m) >= maxChallenges {
		for k, v := range cs.m {
			if now.After(v.expires) {
				delete(cs.m, k)
			}
		}
		if len(cs.m) >= maxChallenges { // all live: drop the oldest
			var oldest string
			var oldestAt time.Time
			for k, v := range cs.m {
				if oldest == "" || v.expires.Before(oldestAt) {
					oldest, oldestAt = k, v.expires
				}
			}
			delete(cs.m, oldest)
		}
	}
	cs.m[id] = c
	return id
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
