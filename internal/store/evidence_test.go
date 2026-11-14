package store

import (
	"context"
	"testing"
	"time"
)

func TestEvidenceSeedsAndReadsBack(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, id := range []string{"a", "b"} {
		if err = st.EnsureAgent(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := st.AgentCount(ctx); err != nil || n != 2 {
		t.Fatalf("agents=%d %v", n, err)
	}
	id, err := st.SeedAction(ctx, SeededAction{AgentID: "a", Kind: "post", Text: "t", ThreadURL: "https://x.invalid/1", PostedAt: at})
	if err != nil || id == 0 {
		t.Fatalf("id=%d %v", id, err)
	}
	if _, err = st.SeedAction(ctx, SeededAction{ID: 40, AgentID: "b", Kind: "post", Text: "t", DryRun: true, PostedAt: at}); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.ActionTally(ctx, ""); got != (ActionTally{Total: 2, Confirmed: 1, DryRun: 1}) {
		t.Fatalf("all=%+v", got)
	}
	if got, _ := st.ActionTally(ctx, "b"); got != (ActionTally{Total: 1, DryRun: 1}) {
		t.Fatalf("b=%+v", got)
	}

	wake, err := st.BeginWake(ctx, "a", at, at.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if err = st.RecordLLMUsage(ctx, "a", wake, "2026-01-01", LLMUsage{TokensIn: 3, TokensOut: 4, CostUSD: 0.5}); err != nil {
		t.Fatal(err)
	}
	if err = st.FinishWake(ctx, wake, at, 2, "completed"); err != nil {
		t.Fatal(err)
	}
	if got, err := st.WakeTotals(ctx, "a", 0); err != nil || got != (WakeTotals{Steps: 2, TokensIn: 3, TokensOut: 4, CostUSD: 0.5, Completed: 1}) {
		t.Fatalf("totals=%+v %v", got, err)
	}
	if got, _ := st.WakeTotals(ctx, "", wake); got.Steps != 2 {
		t.Fatalf("by wake=%+v", got)
	}
	if reason, err := st.WakeEndReason(ctx, wake); err != nil || reason != "completed" {
		t.Fatalf("reason=%q %v", reason, err)
	}
	if recs, err := st.WakeRecords(ctx, "b"); err != nil || len(recs) != 0 {
		t.Fatalf("b wakes=%v %v", recs, err)
	}
	if recs, err := st.WakeRecords(ctx, ""); err != nil || len(recs) != 1 || recs[0].AgentID != "a" || recs[0].TokensOut != 4 {
		t.Fatalf("wakes=%+v %v", recs, err)
	}
	if n, _ := st.MetricsWakeCount(ctx); n != 0 {
		t.Fatalf("metric wakes=%d", n)
	}

	call, err := st.RecordToolAttempt(ctx, "a", wake, "post", "{}", "quota", at)
	if err != nil || call == 0 {
		t.Fatalf("call=%d %v", call, err)
	}
	if names, err := st.ToolCallNames(ctx, "a", wake); err != nil || len(names) != 1 || names[0] != "post" {
		t.Fatalf("names=%v %v", names, err)
	}
	if names, _ := st.ToolCallNames(ctx, "b", 0); len(names) != 0 {
		t.Fatalf("b names=%v", names)
	}
	if blocked, err := st.LastBlockedBy(ctx); err != nil || blocked != "quota" {
		t.Fatalf("blocked=%q %v", blocked, err)
	}

	posts := []SeededPost{{Text: "p", URL: "https://x.invalid/p", PostedAt: at, Measured: at.Add(24 * time.Hour), Likes: 5}}
	if err = st.SeedMeasuredPosts(ctx, "a", posts); err != nil {
		t.Fatal(err)
	}
	if got, err := st.MetricTally(ctx, "a"); err != nil || got != (MetricTally{Samples: 1, CompletedJobs: 3}) {
		t.Fatalf("tally=%+v %v", got, err)
	}
	if got, _ := st.MetricTally(ctx, "b"); got.Foreign != 1 {
		t.Fatalf("foreign=%+v", got)
	}
	if got, _ := st.MetricTally(ctx, "a", id+1); got.Foreign != 0 {
		t.Fatalf("excluded=%+v", got)
	}
	if samples, err := st.AllMetricSamples(ctx); err != nil || len(samples) != 1 || samples[0].AgentID != "a" || samples[0].Likes != 5 {
		t.Fatalf("samples=%+v %v", samples, err)
	}

	if err = st.FailMetricInserts(ctx, true); err != nil {
		t.Fatal(err)
	}
	if err = st.SeedMeasuredPosts(ctx, "a", posts); err == nil {
		t.Fatal("metric insert succeeded while failing")
	}
	if err = st.FailMetricInserts(ctx, false); err != nil {
		t.Fatal(err)
	}
	if err = st.SeedMeasuredPosts(ctx, "a", posts); err != nil {
		t.Fatalf("metric insert failed after clearing: %v", err)
	}
	if n, err := st.StopRequestContacts(ctx, "a"); err != nil || n != 0 {
		t.Fatalf("contacts=%d %v", n, err)
	}
	if err = st.MarkDoNotContact(ctx, "a", "u", "stop_request"); err != nil {
		t.Fatal(err)
	}
	if n, _ := st.StopRequestContacts(ctx, "a"); n != 1 {
		t.Fatalf("contacts=%d", n)
	}
	if err = st.SetAgentStatus(ctx, "a", "paused"); err != nil {
		t.Fatal(err)
	}
	if s, err := st.AgentStatus(ctx, "a"); err != nil || s != "paused" {
		t.Fatalf("status=%q %v", s, err)
	}
}

func TestReadOnlyProbeRejectsWritesOnReadOnlyStore(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/ro.db"
	rw, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer rw.Close()
	ro, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	if u, s := ro.ReadOnlyProbe(ctx); !u || !s {
		t.Fatalf("read-only store accepted a write: update rejected=%v schema rejected=%v", u, s)
	}
	if u, s := rw.ReadOnlyProbe(ctx); u || s {
		t.Fatalf("writable store rejected a write: update=%v schema=%v", u, s)
	}
}
