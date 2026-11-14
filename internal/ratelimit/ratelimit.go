// Package ratelimit is the one per-client limiter and client-address rule
// shared by the dashboard and the API server.
package ratelimit

import (
	"math"
	"net"
	"net/http"
	"sync"
	"time"
)

type tokenBucket struct {
	tokens float64
	last   time.Time
}

// Limiter is a per-client token bucket with a hard bucket cap. Old buckets
// are evicted instead of growing the map with unique client addresses.
type Limiter struct {
	mu      sync.Mutex
	buckets map[string]*tokenBucket
	rate    float64
	burst   float64
	max     int
	now     func() time.Time
}

// New returns a limiter refilling rate tokens per second up to burst, tracking at most max clients.
func New(rate, burst float64, max int, now func() time.Time) *Limiter {
	return &Limiter{buckets: map[string]*tokenBucket{}, rate: rate, burst: burst, max: max, now: now}
}

// Allow takes one token for key, or reports how long until one is available.
func (l *Limiter) Allow(key string) (bool, time.Duration) {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.buckets[key]
	if !ok {
		if len(l.buckets) >= l.max {
			l.evictOldestLocked()
			if len(l.buckets) >= l.max {
				return false, time.Second
			}
		}
		b = &tokenBucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	elapsed := now.Sub(b.last).Seconds()
	if elapsed > 0 {
		b.tokens = math.Min(l.burst, b.tokens+elapsed*l.rate)
		b.last = now
	}
	if b.tokens < 1 {
		retry := time.Second
		if l.rate > 0 {
			retry = time.Duration(math.Ceil((1-b.tokens)/l.rate)) * time.Second
			if retry < time.Second {
				retry = time.Second
			}
		}
		return false, retry
	}
	b.tokens--
	return true, 0
}

func (l *Limiter) evictOldestLocked() {
	var key string
	var at time.Time
	for k, b := range l.buckets {
		if key == "" || b.last.Before(at) {
			key, at = k, b.last
		}
	}
	if key != "" {
		delete(l.buckets, key)
	}
}

// ClientIP names the visitor. Both servers listen on loopback behind the
// Cloudflare tunnel, so a loopback peer is cloudflared and CF-Connecting-IP
// is trusted; from any other peer the header is ignored.
func ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		if v := net.ParseIP(r.Header.Get("CF-Connecting-IP")); v != nil {
			return v.String()
		}
	}
	return host
}
