package web

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// loginLimiter throttles password guessing: after maxFailures failures
// from one source IP, further attempts are rejected until the lockout
// window passes. State is in-memory; a restart clears it, which is an
// acceptable trade for a single-admin dashboard.
type loginLimiter struct {
	mu    sync.Mutex
	fails map[string]*loginFailures
}

type loginFailures struct {
	count       int
	lockedUntil time.Time
}

const (
	loginMaxFailures   = 5
	loginLockoutPeriod = 15 * time.Minute
)

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{fails: make(map[string]*loginFailures)}
}

func (l *loginLimiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	f, ok := l.fails[ip]
	if !ok {
		return true
	}
	if time.Now().Before(f.lockedUntil) {
		return false
	}
	return true
}

func (l *loginLimiter) fail(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	f, ok := l.fails[ip]
	if !ok {
		if len(l.fails) > 10_000 {
			l.fails = make(map[string]*loginFailures)
		}
		f = &loginFailures{}
		l.fails[ip] = f
	}
	f.count++
	if f.count >= loginMaxFailures {
		f.lockedUntil = time.Now().Add(loginLockoutPeriod)
		f.count = 0
	}
}

func (l *loginLimiter) success(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.fails, ip)
}

// clientIP extracts the host part of RemoteAddr.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
