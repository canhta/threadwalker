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
	return fmt.Sprintf(`{"text":"draft %d","target_url":"https://www.threads.com/@a/post/p%d","target_username":"a"}`, i, i)
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
		_, err := d.Dispatch(context.Background(), call("reply", unreadReply(i)))
		reason, hint := blockedResult(t, err)
		if reason != "reply_target_unread" {
			t.Fatalf("refusal %d: %s", i, reason)
		}
		hints = append(hints, hint)
	}
	next := guard.NextAction("reply_target_unread")
	if strings.Contains(hints[0], next) || !strings.Contains(hints[1], next) || !strings.Contains(hints[2], "closed") {
		t.Fatalf("hints: %q", hints)
	}
	// A reply that would pass every check up to the critic is still refused first.
	d.Guard().NoteRead("tid", "https://www.threads.com/@a/post/ok", "")
	_, err = d.Dispatch(context.Background(), call("reply", `{"text":"fine","target_id":"tid","target_url":"https://www.threads.com/@a/post/ok","target_username":"a"}`))
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
	// Calling the closed tool again ends the wake, like a repeated refusal.
	_, err = d.Dispatch(context.Background(), call("reply", unreadReply(9)))
	if !errors.Is(err, tools.ErrStallStop) {
		t.Fatalf("second call to a closed tool: %v", err)
	}
}

// Refusals count per reason: two reasons twice each close nothing.
func TestRefusalCounterIsPerReason(t *testing.T) {
	_, _, d := setup(t)
	for i := 1; i <= 2; i++ {
		d.Dispatch(context.Background(), call("reply", unreadReply(i)))
		d.Dispatch(context.Background(), call("reply", fmt.Sprintf(`{"text":"x","target_url":"https://example.com/p%d","target_username":"a"}`, i)))
	}
	_, err := d.Dispatch(context.Background(), call("reply", unreadReply(3)))
	if reason, _ := blockedResult(t, err); reason != "reply_target_unread" {
		t.Fatalf("reason %s", reason)
	}
}

// fetch_trends is a snapshot: one per wake. The feed gets one retry.
func TestReadCaps(t *testing.T) {
	_, opts, _ := setup(t)
	opts.FetchTrends = func(context.Context) ([]trends.Trend, error) { return []trends.Trend{{Title: "t"}}, nil }
	d, _ := tools.NewDispatcher(opts)
	for name, max := range map[string]int{"fetch_trends": tools.MaxTrendFetchesPerWake, "read_feed": tools.MaxFeedReadsPerWake} {
		for i := 1; i <= max+1; i++ {
			_, err := d.Dispatch(context.Background(), call(name, "{}"))
			if limited := err != nil && strings.Contains(err.Error(), "limit_per_wake"); limited != (i > max) {
				t.Fatalf("%s call %d: %v", name, i, err)
			}
		}
	}
}
