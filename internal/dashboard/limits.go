package dashboard

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"sync"
	"time"
)

// pageResponse is the cacheable outcome of one render.
type pageResponse struct {
	status      int
	body        []byte
	contentType string
	etag        string
}

type cacheEntry struct {
	ready   chan struct{}
	loading bool
	at      time.Time
	resp    pageResponse
	err     bool
}

// pageCache renders each key at most once per TTL and collapses concurrent
// misses through the entry's ready channel. Expired entries stay available for
// a bounded stale window so a database error does not blank a page that was
// rendered moments earlier.
type pageCache struct {
	mu       sync.Mutex
	entries  map[string]*cacheEntry
	ttl      time.Duration
	staleTTL time.Duration
	max      int
	now      func() time.Time
}

func newPageCache(ttl, stale time.Duration, max int, now func() time.Time) *pageCache {
	return &pageCache{entries: map[string]*cacheEntry{}, ttl: ttl, staleTTL: stale, max: max, now: now}
}

func (c *pageCache) do(key string, fn func() (pageResponse, error)) (pageResponse, bool, error) {
	c.mu.Lock()
	now := c.now()
	c.pruneLocked(now)
	e, ok := c.entries[key]
	if ok && e.loading {
		ready := e.ready
		c.mu.Unlock()
		<-ready
		c.mu.Lock()
		resp, failed := e.resp, e.err
		c.mu.Unlock()
		if failed {
			return pageResponse{}, false, errors.New("render failed")
		}
		return resp, false, nil
	}
	if ok && !e.err && now.Sub(e.at) < c.ttl {
		resp := e.resp
		c.mu.Unlock()
		return resp, false, nil
	}
	if ok && e.err && now.Sub(e.at) < time.Second {
		c.mu.Unlock()
		return pageResponse{}, false, errors.New("render failed")
	}
	if !ok {
		if len(c.entries) >= c.max {
			c.evictOldestLocked()
			if len(c.entries) >= c.max {
				c.mu.Unlock()
				resp, err := fn()
				return resp, false, err
			}
		}
		e = &cacheEntry{ready: make(chan struct{}), at: now}
		c.entries[key] = e
	}
	e.loading = true
	e.err = false
	e.ready = make(chan struct{})
	old := e.resp
	oldAt := e.at
	ready := e.ready
	c.mu.Unlock()

	resp, err := fn()

	c.mu.Lock()
	e.loading = false
	if err != nil {
		if c.staleTTL > 0 && len(old.body) > 0 && c.now().Sub(oldAt) <= c.staleTTL {
			e.at = c.now()
			e.resp = old
			close(ready)
			c.mu.Unlock()
			return old, true, nil
		}
		e.err = true
		e.at = c.now()
		close(ready)
		c.mu.Unlock()
		return pageResponse{}, false, err
	}
	e.resp = resp
	e.at = c.now()
	close(ready)
	entry := e.resp
	c.mu.Unlock()
	return entry, false, nil
}

func (c *pageCache) pruneLocked(now time.Time) {
	for key, e := range c.entries {
		if e.loading {
			continue
		}
		if now.Sub(e.at) > c.ttl+c.staleTTL {
			delete(c.entries, key)
		}
	}
}

func (c *pageCache) evictOldestLocked() {
	var oldestKey string
	var oldest time.Time
	for key, e := range c.entries {
		if e.loading {
			continue
		}
		if oldestKey == "" || e.at.Before(oldest) {
			oldestKey, oldest = key, e.at
		}
	}
	if oldestKey != "" {
		delete(c.entries, oldestKey)
	}
}

func (c *pageCache) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

func etagFor(body []byte) string {
	sum := sha256.Sum256(body)
	return `W/"` + hex.EncodeToString(sum[:9]) + `"`
}

// limitedListener caps concurrent accepted connections. It releases a slot
// when the connection closes or Accept fails.
type limitedListener struct {
	inner net.Listener
	sem   chan struct{}
	done  chan struct{}
	once  sync.Once
}

func newLimitedListener(inner net.Listener, max int) net.Listener {
	if max <= 0 {
		return inner
	}
	return &limitedListener{inner: inner, sem: make(chan struct{}, max), done: make(chan struct{})}
}

func (l *limitedListener) Accept() (net.Conn, error) {
	select {
	case l.sem <- struct{}{}:
	case <-l.done:
		return nil, net.ErrClosed
	}
	c, err := l.inner.Accept()
	if err != nil {
		<-l.sem
		return nil, err
	}
	return &limitedConn{Conn: c, release: func() { <-l.sem }}, nil
}

func (l *limitedListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return l.inner.Close()
}

func (l *limitedListener) Addr() net.Addr { return l.inner.Addr() }

type limitedConn struct {
	net.Conn
	release func()
	once    sync.Once
}

func (c *limitedConn) Close() error {
	c.once.Do(c.release)
	return c.Conn.Close()
}
