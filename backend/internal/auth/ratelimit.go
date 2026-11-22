package auth

import (
	"sync"
	"time"
)

// LoginLimiter blocks a key, such as an email and client address, after too many sign-in
// attempts. An attempt counts as soon as it starts, and a success clears the key, so
// requests sent at the same time cannot get past the limit.
// It is in memory: a restart clears the blocks, which is acceptable for a
// self-hosted, single-instance deployment.
type LoginLimiter struct {
	mu          sync.Mutex
	window      time.Duration
	blockFor    time.Duration
	maxAttempts int
	state       map[string]limitState
}

type limitState struct {
	attempts     []time.Time
	blockedUntil time.Time
}

func NewLoginLimiter(window, blockFor time.Duration, maxAttempts int) *LoginLimiter {
	return &LoginLimiter{
		window:      window,
		blockFor:    blockFor,
		maxAttempts: maxAttempts,
		state:       make(map[string]limitState),
	}
}

// Attempt records a sign-in attempt for key and reports whether it may go ahead.
// The attempt that reaches maxAttempts in the window is allowed, and blocks key after it.
func (l *LoginLimiter) Attempt(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweep(now)
	st := l.state[key]
	if now.Before(st.blockedUntil) {
		return false
	}
	st.attempts = append(l.recent(st.attempts, now), now)
	if len(st.attempts) >= l.maxAttempts {
		st.blockedUntil = now.Add(l.blockFor)
		st.attempts = nil
	}
	l.state[key] = st
	return true
}

// Success clears the history for key.
func (l *LoginLimiter) Success(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.state, key)
}

// recent returns a new slice, so callers can inspect it without mutating stored state.
func (l *LoginLimiter) recent(attempts []time.Time, now time.Time) []time.Time {
	kept := make([]time.Time, 0, len(attempts))
	for _, t := range attempts {
		if now.Sub(t) <= l.window {
			kept = append(kept, t)
		}
	}
	return kept
}

// sweep drops entries that no longer affect any decision.
func (l *LoginLimiter) sweep(now time.Time) {
	for k, st := range l.state {
		if now.After(st.blockedUntil) && len(l.recent(st.attempts, now)) == 0 {
			delete(l.state, k)
		}
	}
}
