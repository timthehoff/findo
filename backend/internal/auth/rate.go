package auth

import (
	"sync"
	"time"
)

// Rate allows each key a fixed number of events per window. Findo uses it to
// bound how often one user can make the server open outbound connections. It's
// in memory, so a restart forgives everyone.
type Rate struct {
	mu     sync.Mutex
	n      int
	window time.Duration
	m      map[string]*bucket
	now    func() time.Time
}

type bucket struct {
	start time.Time
	count int
}

func NewRate(n int, window time.Duration) *Rate {
	return &Rate{n: n, window: window, m: map[string]*bucket{}, now: time.Now}
}

// Allow records one event for key and reports whether it is within the limit;
// if not, how long until the window turns over.
func (r *Rate) Allow(key string) (ok bool, retryAfter time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	if len(r.m) > maxTracked {
		for k, b := range r.m {
			if now.Sub(b.start) >= r.window {
				delete(r.m, k)
			}
		}
	}
	b := r.m[key]
	if b == nil || now.Sub(b.start) >= r.window {
		b = &bucket{start: now}
		r.m[key] = b
	}
	if b.count >= r.n {
		return false, b.start.Add(r.window).Sub(now)
	}
	b.count++
	return true, 0
}
