package tools_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/canhta/threadz/internal/threads"
)

func TestGuardedActionMetricsBindOwnedConfirmedAction(t *testing.T) {
	for _, tc := range []struct {
		name, agent, url string
		dry              int
		allowed          bool
	}{
		{"own", "agent", "https://threads.example/own", 0, true},
		{"foreign", "other", "https://threads.example/other", 0, false},
		{"dry_run", "agent", "https://threads.example/dry", 1, false},
		{"unconfirmed", "agent", "", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			h := newBrowserHarness(t, true)
			h.backend.Actions[mediaOf(tc.url)] = threads.Metrics{Views: 5, Likes: 1}
			h.backend.Hook = func(ctx context.Context, c threads.Call) error {
				url := c.Arg
				calls++
				if _, ok := ctx.Deadline(); !ok {
					t.Error("unbounded source call")
				}
				if url != mediaOf(tc.url) {
					t.Errorf("url=%q", url)
				}
				var audit int
				if err := h.store.DB().QueryRow(`SELECT COUNT(*) FROM tool_calls WHERE tool='read_own_metrics' AND result_json=''`).Scan(&audit); err != nil || audit != 1 {
					t.Fatalf("missing pre-effect audit count=%d err=%v", audit, err)
				}
				return nil
			}
			_, err := h.store.DB().Exec(`INSERT INTO actions(id,agent_id,kind,text,dry_run,posted_at,thread_url,media_id) VALUES(11,?,'post','',?,'2026-11-11T00:00:00Z',?,?)`, tc.agent, tc.dry, tc.url, mediaOf(tc.url))
			if err != nil {
				t.Fatal(err)
			}
			out, err := h.call("read_own_metrics", `{"action_id":11}`)
			if tc.allowed {
				if err != nil || !strings.Contains(out, `"action_id":11`) || !strings.Contains(out, `"views":5`) {
					t.Fatalf("%s %v", out, err)
				}
			} else if err == nil || calls != 0 {
				t.Fatalf("invalid binding called source: %s %v calls=%d", out, err, calls)
			}
		})
	}
}
func TestActionMetricsNeverFallsBackToAggregate(t *testing.T) {
	h := newBrowserHarness(t, true)
	if _, err := h.store.DB().Exec(`INSERT INTO actions(id,agent_id,kind,text,dry_run,posted_at,thread_url) VALUES(1,'agent','post','',0,'2026-11-11T00:00:00Z','https://threads.example/own')`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.call("read_own_metrics", `{"action_id":1}`); err == nil {
		t.Fatal("missing optional source accepted")
	}
	if aggregateReads(h.backend) != 0 {
		t.Fatal("aggregate fallback")
	}
	for i, args := range []string{`{"action_id":0}`, `{"action_id":-1}`, `{"action_id":1.1}`, `{"action_id":null}`, `{"action_id":"1"}`, `{"action_id":9223372036854775808}`} {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			if _, err := h.call("read_own_metrics", args); err == nil {
				t.Fatal("invalid id accepted")
			}
		})
	}
}
