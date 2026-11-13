package store_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/store"
)

func openPhaseStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "phases.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestLLMUsagePhaseTagsDreamAndExcludesItFromDailyCap(t *testing.T) {
	ctx := context.Background()
	st := openPhaseStore(t)
	now := time.Date(2026, 1, 1, 23, 0, 0, 0, time.UTC)
	if err := st.EnsureAgent(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	wakeDream, err := st.BeginPhaseWake(ctx, "a", store.WakeDream, "2026-01-01", now)
	if err != nil {
		t.Fatal(err)
	}
	if err = st.FinishWake(ctx, wakeDream, now.Add(time.Minute), 2, "completed"); err != nil {
		t.Fatal(err)
	}
	wakeAgent, err := st.BeginWake(ctx, "a", now.Add(time.Minute), now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if err = st.RecordLLMUsage(ctx, "a", wakeDream, "2026-01-01", store.LLMUsage{CostUSD: 0.20}); err != nil {
		t.Fatal(err)
	}
	if err = st.RecordLLMUsage(ctx, "a", wakeAgent, "2026-01-01", store.LLMUsage{CostUSD: 0.30}); err != nil {
		t.Fatal(err)
	}
	dailyCap, err := st.LLMUsageForDayPhases(ctx, "a", "2026-01-01", "agent", "study")
	if err != nil || dailyCap.CostUSD != 0.30 {
		t.Fatalf("daily-cap spend=%+v err=%v", dailyCap, err)
	}
	dream, err := st.LLMUsageForDayPhases(ctx, "a", "2026-01-01", "dream")
	if err != nil || dream.CostUSD != 0.20 {
		t.Fatalf("dream spend=%+v err=%v", dream, err)
	}
	total, err := st.LLMUsageForDay(ctx, "a", "2026-01-01")
	if err != nil || total.CostUSD != 0.50 {
		t.Fatalf("total spend=%+v err=%v", total, err)
	}
	// Unknown phases fail closed rather than silently under-counting.
	if _, err = st.LLMUsageForDayPhases(ctx, "a", "2026-01-01", "wake"); err == nil {
		t.Fatal("unknown phase accepted")
	}
}

func TestBeginPhaseWakeOncePerKeyAndCount(t *testing.T) {
	ctx := context.Background()
	st := openPhaseStore(t)
	if err := st.EnsureAgent(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 1, 1, 23, 40, 0, 0, time.UTC)
	if _, err := st.BeginPhaseWake(ctx, "a", store.WakeDream, "2026-01-01", now); err != nil {
		t.Fatal(err)
	}
	if _, err := st.BeginPhaseWake(ctx, "a", store.WakeDream, "2026-01-01", now); !errors.Is(err, store.ErrPhaseAlreadyRan) {
		t.Fatalf("duplicate dream err=%v", err)
	}
	if err := st.FinishWake(ctx, 1, now.Add(time.Minute), 2, "completed"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.BeginPhaseWake(ctx, "a", store.WakeStudy, "2026-01-01T23:40", now); err != nil {
		t.Fatal(err)
	}
	count, err := st.CountPhaseWakes(ctx, "a", store.WakeStudy, now.Add(-time.Hour), now.Add(time.Hour))
	if err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	last, ok, err := st.LastPhaseWake(ctx, "a", store.WakeDream)
	if err != nil || !ok || !last.Equal(now) {
		t.Fatalf("last=%v ok=%v err=%v", last, ok, err)
	}
}

func TestTrendStoriesAndThreadsObservationsAreBoundedQueries(t *testing.T) {
	ctx := context.Background()
	st := openPhaseStore(t)
	now := time.Date(2026, 1, 8, 12, 0, 0, 0, time.UTC)
	if err := st.EnsureAgent(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	study := []store.TrendStory{{ObservedAt: now, Source: "news", Title: "Study story", URL: "https://news.invalid/1", Score: 9}}
	agentPhase := []store.TrendStory{{ObservedAt: now.Add(time.Minute), Source: "hn", Title: "Agent story", URL: "https://news.invalid/2", Score: 3}}
	if err := st.RecordTrendStories(ctx, "a", "study", study, now, 10); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordTrendStories(ctx, "a", "agent", agentPhase, now.Add(time.Minute), 10); err != nil {
		t.Fatal(err)
	}
	studyOnly, err := st.RecentStudyTrends(ctx, "a", now.Add(-24*time.Hour), 10)
	if err != nil || len(studyOnly) != 1 || studyOnly[0].Title != "Study story" {
		t.Fatalf("study stories=%+v err=%v", studyOnly, err)
	}
	all, err := st.RecentTrendStories(ctx, "a", now.Add(-24*time.Hour), 10)
	if err != nil || len(all) != 2 || all[0].Score != 3 {
		t.Fatalf("all stories=%+v err=%v", all, err)
	}

	replies := int64(12)
	observations := []store.ThreadsObservation{
		{PostURL: "https://threads.net/@a/post/1", Author: "a", TextHash: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", Snippet: "AI take", Replies: &replies, Kind: "feed"},
		{PostURL: "https://threads.net/@b/post/2", Author: "b", TextHash: "1123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", Snippet: "other take", Kind: "search", Keyword: "AI", ObservedAt: now},
	}
	if err = st.RecordThreadsObservations(ctx, "a", observations, now); err != nil {
		t.Fatal(err)
	}
	got, err := st.RecentThreadsObservations(ctx, "a", now.Add(-time.Hour), 10)
	if err != nil || len(got) != 2 {
		t.Fatalf("observations=%+v err=%v", got, err)
	}
	if got[0].Replies != nil || got[1].Replies == nil || *got[1].Replies != 12 {
		t.Fatalf("nullable counters not preserved: %+v", got)
	}
	if err = st.RecordThreadsObservations(ctx, "a", []store.ThreadsObservation{{PostURL: "x", Author: "a", TextHash: "short", Snippet: "x", Kind: "feed"}}, now); err == nil {
		t.Fatal("invalid observation accepted")
	}
}
