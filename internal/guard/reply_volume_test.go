package guard_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/store"
)

func volumeReply(seq int) map[string]any {
	return replyArgs(seq, fmt.Sprintf("user%d", seq), fmt.Sprintf("https://threads.com/@user%d/post/%d", seq, seq))
}

func TestSeventhReplyInAnHourIsRefused(t *testing.T) {
	h := newPolicyHarness(t, func(tmpl *config.Template) { tmpl.Limits.RepliesPerHour = 6 })
	for i := 1; i <= 6; i++ {
		h.allow("reply", volumeReply(i))
		h.clock.Advance(2 * time.Second)
	}
	h.blocked("reply", volumeReply(7), "replies_per_hour")
	h.allow("post", postArgs(1))
	h.clock.Advance(61 * time.Minute)
	h.allow("reply", volumeReply(8))
}

// seedMeasured inserts n measured live-mode posts, newest first an hour back, each with likes.
func seedMeasured(t *testing.T, h *policyHarness, n, likes int) {
	t.Helper()
	for i := 0; i < n; i++ {
		id, err := h.st.SeedAction(context.Background(), store.SeededAction{AgentID: "guard-agent", Kind: "post", Text: fmt.Sprintf("seed %d", i), DryRun: true, PostedAt: h.clock.Now().Add(-time.Duration(i+1) * time.Minute)})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = h.st.DB().Exec(`INSERT INTO metrics(agent_id,action_id,measured_at,likes) VALUES('guard-agent',?,?,?)`, id, h.clock.Now().Format(time.RFC3339), likes); err != nil {
			t.Fatal(err)
		}
	}
}

func breakerNotices(t *testing.T, h *policyHarness) int {
	t.Helper()
	var n int
	if err := h.st.DB().QueryRow(`SELECT COUNT(*) FROM owner_msgs WHERE agent_id='guard-agent' AND event_key LIKE 'notice:reply_breaker:%'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestBreakerPausesRepliesNotPostsAndQueuesOneNotice(t *testing.T) {
	h := newPolicyHarness(t, func(tmpl *config.Template) {
		tmpl.Limits.BreakerMinLikes, tmpl.Limits.BreakerActions, tmpl.Limits.BreakerHours = 2, 15, 6
	})
	seedMeasured(t, h, 15, 0)
	h.blocked("reply", volumeReply(1), "reply_breaker")
	h.blocked("reply", volumeReply(2), "reply_breaker")
	if n := breakerNotices(t, h); n != 1 {
		t.Fatalf("notices %d, want 1", n)
	}
	if hint := h.lastHint(t); !strings.Contains(hint, "2026-11-11T16:00:00Z") {
		t.Fatalf("hint %q", hint)
	}
	h.allow("post", postArgs(1))
	h.clock.Advance(6*time.Hour + time.Minute)
	h.allow("reply", volumeReply(3)) // only actions after the pause began count toward a new trip
}

func (h *policyHarness) lastHint(t *testing.T) string {
	t.Helper()
	_, err := h.call("reply", volumeReply(99))
	_, hint := modelResult(t, err)
	return hint
}

func TestBreakerNeedsAFullWindowAndEnoughLikesToStayOpen(t *testing.T) {
	cfg := func(tmpl *config.Template) {
		tmpl.Limits.BreakerMinLikes, tmpl.Limits.BreakerActions, tmpl.Limits.BreakerHours = 2, 15, 6
	}
	h := newPolicyHarness(t, cfg)
	seedMeasured(t, h, 14, 0)
	h.allow("reply", volumeReply(1))
	if n := breakerNotices(t, h); n != 0 {
		t.Fatalf("notices %d on a short window", n)
	}
	h = newPolicyHarness(t, cfg)
	seedMeasured(t, h, 15, 1) // 15 likes in total
	h.allow("reply", volumeReply(1))
}
