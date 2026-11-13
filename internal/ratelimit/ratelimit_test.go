package ratelimit

import (
	"net/http/httptest"
	"testing"
	"time"
)

func TestLimiterRefillsAndCapsBuckets(t *testing.T) {
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	l := New(1, 2, 2, func() time.Time { return now })
	if ok, _ := l.Allow("a"); !ok {
		t.Fatal("first request denied")
	}
	if ok, _ := l.Allow("a"); !ok {
		t.Fatal("second request denied")
	}
	if ok, retry := l.Allow("a"); ok || retry < time.Second {
		t.Fatalf("third request allowed or retry %v", retry)
	}
	now = now.Add(time.Second)
	if ok, _ := l.Allow("a"); !ok {
		t.Fatal("refill did not allow")
	}
	l.Allow("b")
	l.Allow("c")
	if len(l.buckets) > 2 {
		t.Fatalf("bucket cap = %d", len(l.buckets))
	}
}

func TestClientIPTrustsTheHeaderOnlyFromLoopback(t *testing.T) {
	for _, tc := range []struct{ peer, header, want string }{
		{"127.0.0.1:5", "203.0.113.9", "203.0.113.9"},
		{"[::1]:5", "203.0.113.9", "203.0.113.9"},
		{"127.0.0.1:5", "garbage", "127.0.0.1"},
		{"127.0.0.1:5", "", "127.0.0.1"},
		{"198.51.100.1:5", "203.0.113.50", "198.51.100.1"},
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = tc.peer
		r.Header.Set("CF-Connecting-IP", tc.header)
		if got := ClientIP(r); got != tc.want {
			t.Errorf("peer %s header %q = %q, want %q", tc.peer, tc.header, got, tc.want)
		}
	}
}
