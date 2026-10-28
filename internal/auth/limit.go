package auth

import (
	"sync"
	"time"
)

// Failed-login limiter defaults: at most LoginMaxFailures per client address within LoginWindow.
const (
	LoginMaxFailures = 10
	LoginWindow      = 5 * time.Minute
)

// Limiter throttles repeated failed sign-ins per client address. Failures
// expire only with time, not on success: otherwise a user with an own account
// could keep resetting the counter while guessing other passwords.
type Limiter struct {
	max    int
	window time.Duration
	now    func() time.Time

	mu    sync.Mutex
	fails map[string][]time.Time
}

// maxLimiterAddrs bounds memory with very many addresses.
const maxLimiterAddrs = 10000

// NewLimiter allows max failures per address within window.
func NewLimiter(max int, window time.Duration) *Limiter {
	return &Limiter{max: max, window: window, now: time.Now, fails: map[string][]time.Time{}}
}

// Blocked reports whether the address is currently blocked.
func (l *Limiter) Blocked(addr string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.recentLocked(addr)) >= l.max
}

// Fail records a failure.
func (l *Limiter) Fail(addr string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.fails[addr] = append(l.recentLocked(addr), l.now())
	if len(l.fails) > maxLimiterAddrs {
		for k := range l.fails {
			l.recentLocked(k)
		}
	}
}

// recentLocked returns failures within the window and drops older ones.
func (l *Limiter) recentLocked(addr string) []time.Time {
	cut := l.now().Add(-l.window)
	in := l.fails[addr]
	out := in[:0]
	for _, t := range in {
		if t.After(cut) {
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		delete(l.fails, addr)
		return nil
	}
	l.fails[addr] = out
	return out
}

// Revocations lists signed-out sessions (SessionID until their tokens' latest
// expiry). In memory only: after a restart unexpired revoked tokens work again.
type Revocations struct {
	now func() time.Time

	mu    sync.Mutex
	until map[string]time.Time
}

// maxRevocations caps the list; when full, Revoke returns false and the
// caller ends all of the user's sessions (token version).
const maxRevocations = 100000

// NewRevocations returns an empty revocation list.
func NewRevocations() *Revocations {
	return &Revocations{now: time.Now, until: map[string]time.Time{}}
}

// Revoke revokes sessionID until until; false means the list is still full
// after pruning and nothing was revoked.
func (r *Revocations) Revoke(sessionID string, until time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	for id, t := range r.until {
		if !now.Before(t) {
			delete(r.until, id)
		}
	}
	if len(r.until) >= maxRevocations {
		return false
	}
	if until.After(r.until[sessionID]) {
		r.until[sessionID] = until
	}
	return true
}

// Revoked reports whether the session is revoked.
func (r *Revocations) Revoked(sessionID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.until[sessionID]
	return ok && r.now().Before(t)
}
