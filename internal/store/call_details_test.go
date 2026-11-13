package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/store"
)

func TestCallDetailsJoinTheAuditAndAreKeptAsLongAsTranscripts(t *testing.T) {
	s, _ := retentionStore(t)
	ctx := context.Background()
	old, err := s.RecordToolAttempt(ctx, "a", 1, "post", "{}", "no_trend_anchor", retentionNow.Add(-9*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := s.RecordToolAttempt(ctx, "a", 1, "read_feed", "{}", "", retentionNow.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RecordCallArgs(ctx, "a", 1, old, `{"text":"old"}`, retentionNow.Add(-9*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err = s.RecordCallArgs(ctx, "a", 1, fresh, `{}`, retentionNow.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err = s.RecordCallDetail(ctx, fresh, store.CallDetail{Counts: map[string]int{"posts": 12}, Ref: &store.RefTrace{Field: "trend_ref", Given: "x"}}); err != nil {
		t.Fatal(err)
	}
	if err = s.RecordToolResult(ctx, fresh, `{"outcome":"success"}`); err != nil {
		t.Fatal(err)
	}
	if got := prune(t, s)["tool_call_details"]; got != 1 {
		t.Fatalf("pruned %d details", got)
	}
	calls, err := s.WakeCalls(ctx, "a", 1)
	if err != nil || len(calls) != 2 {
		t.Fatalf("%d calls, %v", len(calls), err)
	}
	if calls[0].BlockedBy != "no_trend_anchor" || calls[0].Args != nil {
		t.Fatalf("the audit row must outlive its details: %+v", calls[0])
	}
	if calls[1].Detail.Counts["posts"] != 12 || calls[1].Detail.Ref.Given != "x" || string(calls[1].Result) != `{"outcome":"success"}` {
		t.Fatalf("%+v", calls[1])
	}
	if age, _ := store.RetentionAge("tool_call_details"); age != 8*24*time.Hour {
		t.Fatalf("details kept %v", age)
	}
}
