package dashboard_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/canhta/threadz/internal/dashboard"
	"github.com/canhta/threadz/internal/store"
)

// gateSource counts reads and holds each one until release is closed.
type gateSource struct {
	reads   atomic.Int64
	entered chan struct{}
	release chan struct{}
}

func newGate() *gateSource {
	return &gateSource{entered: make(chan struct{}, 64), release: make(chan struct{})}
}

func (g *gateSource) DashboardSnapshot(ctx context.Context, q store.DashboardQuery) (store.DashboardSnapshot, error) {
	g.reads.Add(1)
	g.entered <- struct{}{}
	select {
	case <-g.release:
	case <-ctx.Done():
	}
	return started(), nil
}

func TestCachedPageIsServedWithoutAStoreRead(t *testing.T) {
	src := newSource(measured())
	h := newHandler(src)
	for i := 0; i < 5; i++ {
		if rr := get(h, "/"); rr.Code != 200 {
			t.Fatalf("request %d: %d", i, rr.Code)
		}
	}
	if len(src.queries) != 1 {
		t.Fatalf("%d store reads for 5 identical requests", len(src.queries))
	}
}

func TestConcurrentMissesShareOneStoreRead(t *testing.T) {
	g := newGate()
	h := newHandler(g)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if rr := get(h, "/"); rr.Code != 200 {
				t.Errorf("status %d", rr.Code)
			}
		}()
	}
	<-g.entered
	close(g.release)
	wg.Wait()
	if n := g.reads.Load(); n != 1 {
		t.Fatalf("%d store reads for concurrent identical misses", n)
	}
}

func TestJunkQueryKeysNeitherReadNorFillTheCache(t *testing.T) {
	src := newSource(started())
	h := newHandler(src, func(o *dashboard.Options) { o.RatePerSec, o.RateBurst = 1000, 1000 })
	get(h, "/")
	for i := 0; i < 50; i++ {
		// An unknown key is ignored, so the request is the default page and
		// answers from the entry the first get filled.
		if rr := get(h, fmt.Sprintf("/?junk%d=%d", i, i)); rr.Code != 200 {
			t.Fatalf("junk key rejected: %d", rr.Code)
		}
		if rr := get(h, fmt.Sprintf("/?agent=alpha&range=%d", 100+i)); rr.Code != 400 {
			t.Fatalf("junk range accepted: %d", rr.Code)
		}
	}
	if len(src.queries) != 1 {
		t.Fatalf("junk requests read the store: %d reads", len(src.queries))
	}
}

func TestGlobalRenderCapAnswers503(t *testing.T) {
	g := newGate()
	h := newHandler(g, func(o *dashboard.Options) { o.MaxConcurrent = 1 })
	done := make(chan struct{})
	go func() { get(h, "/"); close(done) }()
	<-g.entered
	rr := get(h, "/?range=14")
	if rr.Code != 503 || rr.Header().Get("Retry-After") == "" {
		t.Fatalf("over the cap: %d %q", rr.Code, rr.Header().Get("Retry-After"))
	}
	close(g.release)
	<-done
}
