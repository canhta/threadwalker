package tools_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/canhta/threadz/internal/guard"
	"github.com/canhta/threadz/internal/quality"
	"github.com/canhta/threadz/internal/tools"
	"github.com/canhta/threadz/internal/trends"
)

func unreadReply(i int) string {
	return fmt.Sprintf(`{"text":"draft %d","target":"p%d"}`, i, 900+i)
}

func blockedResult(t *testing.T, err error) (reason, hint string) {
	t.Helper()
	var b *guard.BlockedError
	if !errors.As(err, &b) {
		t.Fatalf("not a refusal: %v", err)
	}
	return b.Reason, guard.Hint(b.Reason, b.Detail) + " " + b.Next
}

// The 2nd refusal of a tool for one reason names the next action, the 3rd
// closes the tool: later calls are refused at once, before any writer or critic call.
func TestRefusalCounterClosesATool(t *testing.T) {
	st, opts, _ := setup(t)
	critics := 0
	opts.Scope.Critic = func(context.Context, quality.CriticInput) (quality.CriticVerdict, error) {
		critics++
		return quality.CriticVerdict{}, errors.New("unavailable")
	}
	d, err := tools.NewDispatcher(opts)
	if err != nil {
		t.Fatal(err)
	}
	var hints []string
	for i := 1; i <= 3; i++ {
		// The same target each time: a refusal counts per (reason, target).
		_, err := d.Dispatch(context.Background(), call("reply", strings.Replace(unreadReply(1), "draft 1", fmt.Sprintf("draft %d", i), 1)))
		reason, hint := blockedResult(t, err)
		if reason != "unknown_target" {
			t.Fatalf("refusal %d: %s", i, reason)
		}
		hints = append(hints, hint)
	}
	next := guard.NextAction("unknown_target")
	if strings.Contains(hints[0], next) || !strings.Contains(hints[1], next) || !strings.Contains(hints[2], "closed") {
		t.Fatalf("hints: %q", hints)
	}
	// A reply that would pass every check up to the critic is still refused first.
	_, err = d.Dispatch(context.Background(), call("reply", `{"text":"fine","target":"`+readRef(t, d.Guard(), "https://www.threads.com/@a/post/ok", "tid")+`"}`))
	if reason, _ := blockedResult(t, err); reason != "tool_closed" || critics != 0 {
		t.Fatalf("closed tool: %v critic calls %d", err, critics)
	}
	// Other tools stay open; the closed one is counted in the audit.
	if _, err := d.Dispatch(context.Background(), call("fetch_trends", "{}")); err != nil {
		t.Fatalf("other tool: %v", err)
	}
	var n int
	st.DB().QueryRow(`SELECT COUNT(*) FROM tool_calls WHERE tool='reply' AND blocked_by='tool_closed'`).Scan(&n)
	if n != 1 {
		t.Fatalf("tool_closed audits %d", n)
	}
	// Calling the closed tool again is refused again, and never ends the wake.
	for range 3 {
		_, err = d.Dispatch(context.Background(), call("reply", unreadReply(9)))
		if reason, _ := blockedResult(t, err); reason != "tool_closed" || errors.Is(err, tools.ErrStallStop) {
			t.Fatalf("later call to a closed tool: %v", err)
		}
	}
}

// Refusals count per reason: two reasons twice each close nothing.
func TestRefusalCounterIsPerReason(t *testing.T) {
	_, _, d := setup(t)
	for i := 1; i <= 2; i++ {
		d.Dispatch(context.Background(), call("reply", unreadReply(i)))
		d.Dispatch(context.Background(), call("reply", fmt.Sprintf(`{"text":"x","target":%q}`, readRef(t, d.Guard(), fmt.Sprintf("https://example.com/p%d", i), ""))))
	}
	_, err := d.Dispatch(context.Background(), call("reply", unreadReply(3)))
	if reason, _ := blockedResult(t, err); reason != "unknown_target" {
		t.Fatalf("reason %s", reason)
	}
}

// fetch_trends is a snapshot: one per wake. The feed gets one retry.
func TestReadCaps(t *testing.T) {
	_, opts, _ := setup(t)
	opts.FetchTrends = func(context.Context) ([]trends.Trend, error) { return []trends.Trend{{Title: "t"}}, nil }
	d, _ := tools.NewDispatcher(opts)
	for _, c := range []struct {
		name, args string
		max        int
	}{{"fetch_trends", "{}", tools.MaxTrendFetchesPerWake}, {"find_posts", `{"source":"feed"}`, tools.MaxFeedReadsPerWake}} {
		name, max := c.name, c.max
		for i := 1; i <= max+1; i++ {
			_, err := d.Dispatch(context.Background(), call(name, c.args))
			if limited := err != nil && strings.Contains(err.Error(), "limit_per_wake"); limited != (i > max) {
				t.Fatalf("%s call %d: %v", name, i, err)
			}
		}
	}
}
