package web

import (
	"net"
	"net/http"
	"strings"
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

// clientIP resolves the originating client address. Forwarded headers
// are honored only when the direct peer is a local/private proxy
// (Traefik on the host, or Cloudflare via Traefik), so an internet
// client hitting the origin directly cannot spoof its address to evade
// the login lockout or poison audit logs.
//
// Preference order behind a trusted proxy:
//  1. CF-Connecting-IP — Cloudflare overwrites this at the edge, so it
//     is the real client even through Cloudflare → Traefik → app.
//  2. True-Client-IP — Cloudflare Enterprise equivalent.
//  3. X-Real-IP — set by many reverse proxies.
//  4. X-Forwarded-For, leftmost entry — the original client.
func clientIP(r *http.Request) string {
	if isTrustedPeer(r.RemoteAddr) {
		for _, h := range []string{"CF-Connecting-IP", "True-Client-IP", "X-Real-IP"} {
			if ip := validIP(r.Header.Get(h)); ip != "" {
				return ip
			}
		}
		if ip := leftmostValidIP(r.Header.Get("X-Forwarded-For")); ip != "" {
			return ip
		}
	}
	return remoteHost(r.RemoteAddr)
}

// isTrustedPeer reports whether the immediate TCP peer is loopback or a
// private/link-local address, i.e. infrastructure we run ourselves.
func isTrustedPeer(remoteAddr string) bool {
	ip := net.ParseIP(remoteHost(remoteAddr))
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
}

func remoteHost(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return remoteAddr
	}
	return host
}

func validIP(s string) string {
	s = strings.TrimSpace(s)
	if net.ParseIP(s) != nil {
		return s
	}
	return ""
}

// leftmostValidIP returns the first syntactically valid IP in a
// comma-separated X-Forwarded-For list.
func leftmostValidIP(xff string) string {
	for _, part := range strings.Split(xff, ",") {
		if ip := validIP(part); ip != "" {
			return ip
		}
	}
	return ""
}
