package web

import (
	"testing"
	"time"
)

func TestLoginLimiter(t *testing.T) {
	l := newLoginLimiter()
	ip := "203.0.113.7"

	for i := 0; i < 4; i++ {
		if !l.allow(ip) {
			t.Fatalf("attempt %d should be allowed", i+1)
		}
		l.fail(ip)
	}
	if !l.allow(ip) {
		t.Fatal("4 failures should not lock out yet")
	}
	l.fail(ip) // 5th → lockout
	if l.allow(ip) {
		t.Fatal("5 failures should trigger lockout")
	}

	// A different IP is unaffected.
	if !l.allow("198.51.100.9") {
		t.Fatal("lockout must be per-IP")
	}

	// Lockout expires.
	f := l.fails[ip]
	f.lockedUntil = time.Now().Add(-time.Second)
	if !l.allow(ip) {
		t.Fatal("expired lockout should allow again")
	}

	// Success clears the record.
	l.fail(ip)
	l.fail(ip)
	l.success(ip)
	if !l.allow(ip) {
		t.Fatal("success should reset failures")
	}
}
