package guard_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/canhta/threadwalker/internal/guard"
	"github.com/canhta/threadwalker/internal/store"
)

func TestSharedPromotionUsesCommittedIntentAndCanonicalURL(t *testing.T) {
	for _, tc := range []struct {
		name, second string
		blocked      bool
	}{
		{"host port path fragment query aliases", "http://www.EXAMPLE.org:80/a/../article/?b=2&a=1#other", true},
		{"bare link", "example.org/article?a=1&b=2", true},
		{"different query", "https://example.org/article?a=2&b=2", false},
		{"different path case", "https://example.org/Article?a=1&b=2", false},
		{"different host", "https://unrelated.org/article?a=1&b=2", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, opts := setup(t)
			opts.Template.Limits.PostsPerDay = 5
			opts.Template.Limits.RepliesPerDay = 10
			opts.Template.Limits.RepliesPerUser7d = 2
			opts.Template.Limits.MinSecondsBetweenActions = 1
			g, err := guard.New(opts)
			if err != nil {
				t.Fatal(err)
			}
			_, err = g.Execute(context.Background(), guard.Attempt{Tool: "post", Arguments: `{"text":"https://example.org/article?a=1&b=2"}`}, func() (string, error) { return "", errors.New("uncertain fixture effect") })
			if err == nil {
				t.Fatal("missing uncertain effect")
			}
			if _, err = st.DB().Exec(`INSERT INTO agents(id,status) VALUES('other','running'); INSERT INTO wakes(id,agent_id,started_at) VALUES(2,'other','2026-11-11T00:00:00Z')`); err != nil {
				t.Fatal(err)
			}
			opts.Template.ID = "other"
			opts.WakeID = 2
			g, err = guard.New(opts)
			if err != nil {
				t.Fatal(err)
			}
			args, _ := json.Marshal(map[string]string{"text": tc.second})
			called := false
			_, err = g.Execute(context.Background(), guard.Attempt{Tool: "post", Arguments: string(args)}, func() (string, error) { called = true; return "ok", nil })
			if tc.blocked {
				if !errors.Is(err, guard.ErrBlocked) || called || !strings.Contains(err.Error(), "shared_promotion_link") {
					t.Fatalf("shared link escaped: %v", err)
				}
			} else if err != nil || !called {
				t.Fatalf("independent link refused: %v", err)
			}
		})
	}
}

func TestConcurrentAgentsReserveSharedPromotionOnce(t *testing.T) {
	st, path, opts := setupPath(t)
	if _, err := st.DB().Exec(`INSERT INTO agents(id,status) VALUES('other','running'); INSERT INTO wakes(id,agent_id,started_at) VALUES(2,'other','2026-11-11T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	opts.Template.Limits.PostsPerDay = 5
	opts.Template.Limits.RepliesPerDay = 10
	opts.Template.Limits.RepliesPerUser7d = 2
	opts.Template.Limits.MinSecondsBetweenActions = 1
	stores := []*store.Store{concurrentStore(t, path), concurrentStore(t, path)}
	var effects atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		candidate := opts
		candidate.Store = stores[i]
		if i == 1 {
			candidate.Template.ID = "other"
			candidate.WakeID = 2
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			g, err := guard.New(candidate)
			if err != nil {
				t.Error(err)
				return
			}
			_, err = g.Execute(context.Background(), guard.Attempt{Tool: "post", Arguments: `{"text":"https://fixture.invalid/shared"}`}, func() (string, error) { effects.Add(1); return "ok", nil })
			if err != nil && !errors.Is(err, guard.ErrBlocked) && !errors.Is(err, guard.ErrAudit) {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if effects.Load() != 1 {
		t.Fatalf("effects=%d", effects.Load())
	}
	counts, err := st.CountActions(context.Background(), "agent", opts.Now().Add(-time.Hour), opts.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	other, err := st.CountActions(context.Background(), "other", opts.Now().Add(-time.Hour), opts.Now().Add(time.Hour))
	if err != nil || counts.Posts+other.Posts != 1 {
		t.Fatalf("reservations %v %v %v", counts, other, err)
	}
}

func TestBareDomainPromotionSharesHTTPSReservation(t *testing.T) {
	st, opts := setup(t)
	opts.Template.Limits.PostsPerDay = 5
	opts.Template.Limits.RepliesPerDay = 5
	opts.Template.Limits.RepliesPerUser7d = 2
	opts.Template.Limits.MinSecondsBetweenActions = 1
	g, err := guard.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = g.Execute(context.Background(), guard.Attempt{Tool: "post", Arguments: `{"text":"https://example.org"}`}, func() (string, error) { return "ok", nil }); err != nil {
		t.Fatal(err)
	}
	if _, err = st.DB().Exec(`INSERT INTO agents(id,status) VALUES('other','running'); INSERT INTO wakes(id,agent_id,started_at) VALUES(2,'other','2026-11-11T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	opts.Template.ID = "other"
	opts.WakeID = 2
	g, err = guard.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	_, err = g.Execute(context.Background(), guard.Attempt{Tool: "post", Arguments: `{"text":"example.org"}`}, func() (string, error) { t.Fatal("duplicate bare link effect"); return "", nil })
	if !errors.Is(err, guard.ErrBlocked) {
		t.Fatal("bare domain promotion escaped")
	}
}
