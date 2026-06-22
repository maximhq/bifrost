package handlers

import (
	"strings"
	"sync"
	"time"

	"github.com/valyala/fasthttp"
)

const (
	maxLoginAttemptsPerKey    = 10
	maxLoginAttemptsGlobal    = 1000
	maxStoredLoginAttemptKeys = 1024
	loginAttemptWindow        = 2 * time.Minute
	loginAttemptLockout       = 2 * time.Minute
)

type loginAttemptLimiter struct {
	mu        sync.Mutex
	attempts  map[string]loginAttemptEntry
	global    loginAttemptEntry
	now       func() time.Time
	max       int
	globalMax int
	window    time.Duration
	lockout   time.Duration
}

type loginAttemptEntry struct {
	count        int
	firstAttempt time.Time
	lockedUntil  time.Time
}

func newLoginAttemptLimiter() *loginAttemptLimiter {
	return &loginAttemptLimiter{
		attempts:  make(map[string]loginAttemptEntry),
		now:       time.Now,
		max:       maxLoginAttemptsPerKey,
		globalMax: maxLoginAttemptsGlobal,
		window:    loginAttemptWindow,
		lockout:   loginAttemptLockout,
	}
}

func (l *loginAttemptLimiter) isAllowed(key string) (time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	l.pruneExpiredLocked(now)

	if retryAfter, locked := lockoutRemaining(l.global, now); locked {
		return retryAfter, false
	}
	entry, ok := l.attempts[key]
	if !ok {
		return 0, true
	}
	if retryAfter, locked := lockoutRemaining(entry, now); locked {
		return retryAfter, false
	}
	return 0, true
}

func (l *loginAttemptLimiter) recordFailure(key string) (time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	l.pruneExpiredLocked(now)

	entry, hasEntry := l.attempts[key]
	if hasEntry || len(l.attempts) < maxStoredLoginAttemptKeys {
		entry = l.recordFailureLocked(entry, l.max, now)
		l.attempts[key] = entry
	}

	l.global = l.recordFailureLocked(l.global, l.globalMax, now)

	usernameRetryAfter, usernameLocked := lockoutRemaining(entry, now)
	globalRetryAfter, globalLocked := lockoutRemaining(l.global, now)
	if globalLocked && globalRetryAfter > usernameRetryAfter {
		return globalRetryAfter, true
	}
	if usernameLocked {
		return usernameRetryAfter, true
	}
	return 0, false
}

func (l *loginAttemptLimiter) recordSuccess(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	delete(l.attempts, key)
	l.global = loginAttemptEntry{}
}

func (l *loginAttemptLimiter) recordFailureLocked(entry loginAttemptEntry, maxAttempts int, now time.Time) loginAttemptEntry {
	if entry.firstAttempt.IsZero() || now.Sub(entry.firstAttempt) > l.window {
		entry = loginAttemptEntry{firstAttempt: now}
	}
	entry.count++
	if entry.count >= maxAttempts {
		entry.lockedUntil = now.Add(l.lockout)
	}
	return entry
}

func (l *loginAttemptLimiter) pruneExpiredLocked(now time.Time) {
	for key, entry := range l.attempts {
		if entry.lockedUntil.After(now) {
			continue
		}
		if !entry.firstAttempt.IsZero() && now.Sub(entry.firstAttempt) <= l.window {
			continue
		}
		delete(l.attempts, key)
	}
	if !l.global.lockedUntil.After(now) && !l.global.firstAttempt.IsZero() && now.Sub(l.global.firstAttempt) > l.window {
		l.global = loginAttemptEntry{}
	}
}

func loginAttemptKey(ctx *fasthttp.RequestCtx, username string) string {
	if forwardedIP := clientForwardedIP(ctx); forwardedIP != "" {
		return "ip:" + strings.ToLower(strings.TrimSpace(forwardedIP))
	}

	key := strings.ToLower(strings.TrimSpace(username))
	if key == "" {
		return "username:<empty>"
	}
	return "username:" + key
}

func lockoutRemaining(entry loginAttemptEntry, now time.Time) (time.Duration, bool) {
	if entry.lockedUntil.After(now) {
		return entry.lockedUntil.Sub(now), true
	}
	return 0, false
}
