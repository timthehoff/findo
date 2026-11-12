package auth

import (
	"sync"
	"time"
)

// Limiter slows down guessing at sign-in and enrollment, per client address.
// The first few failures are free; after that each further failure doubles the
// wait before that address may try again, up to a cap. A success clears it.
// It's in memory: a restart forgives everyone, which is fine for a brake.
type Limiter struct {
	mu  sync.Mutex
	m   map[string]*attempts
	now func() time.Time
}

type attempts struct {
	failures int
	until    time.Time // no attempts before this
	last     time.Time
}

const (
	freeFailures = 5
	baseDelay    = time.Second
	maxDelay     = 5 * time.Minute
	forgetAfter  = 30 * time.Minute
	maxTracked   = 10000
)

func NewLimiter() *Limiter { return &Limiter{m: map[string]*attempts{}, now: time.Now} }

// Allow reports whether key may try now; if not, how long to wait.
func (l *Limiter) Allow(key string) (ok bool, retryAfter time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	a := l.m[key]
	if a == nil {
		return true, 0
	}
	now := l.now()
	if now.Sub(a.last) > forgetAfter {
		delete(l.m, key)
		return true, 0
	}
	if now.Before(a.until) {
		return false, a.until.Sub(now)
	}
	return true, 0
}

// Fail records a failed attempt from key.
func (l *Limiter) Fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if len(l.m) >= maxTracked {
		for k, a := range l.m {
			if now.Sub(a.last) > forgetAfter {
				delete(l.m, k)
			}
		}
		if len(l.m) >= maxTracked { // under attack from many addresses: start over
			l.m = map[string]*attempts{}
		}
	}
	a := l.m[key]
	if a == nil {
		a = &attempts{}
		l.m[key] = a
	}
	a.failures++
	a.last = now
	if a.failures > freeFailures {
		d := baseDelay << uint(min(a.failures-freeFailures-1, 20))
		if d > maxDelay {
			d = maxDelay
		}
		a.until = now.Add(d)
	}
}

// Succeed clears key's failures.
func (l *Limiter) Succeed(key string) {
	l.mu.Lock()
	delete(l.m, key)
	l.mu.Unlock()
}
