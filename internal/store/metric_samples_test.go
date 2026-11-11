package store_test

import (
	"context"
	"github.com/canhta/threadwalker/internal/store"
	"testing"
)

func TestRecentMetricSamplesBoundedExactTimeAndValidation(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err = st.DB().Exec(`INSERT INTO actions(id,agent_id,kind,text,dry_run,posted_at,thread_url) VALUES(1,'own','post','',0,'2026-01-01T00:00:00Z','https://fixture.test/own')`); err != nil {
		t.Fatal(err)
	}
	for _, at := range []string{"2026-01-02T00:00:00Z", "2026-01-02T00:00:00.000000001Z", "2026-01-02T00:00:00.000000002Z"} {
		if _, err = st.DB().Exec(`INSERT INTO metrics(agent_id,action_id,measured_at,views) VALUES('own',1,?,42)`, at); err != nil {
			t.Fatal(err)
		}
	}
	samples, err := st.RecentMetricSamples(ctx, "own", 2)
	if err != nil || len(samples) != 2 {
		t.Fatalf("samples=%+v err=%v", samples, err)
	}
	if samples[0].MeasuredAt.Nanosecond() != 2 || samples[1].MeasuredAt.Nanosecond() != 1 {
		t.Fatalf("out of order: %+v", samples)
	}
	for _, tc := range []struct {
		id    string
		limit int
	}{{"", 10}, {"own", 0}, {"own", 101}} {
		if _, err = st.RecentMetricSamples(ctx, tc.id, tc.limit); err == nil {
			t.Fatal("invalid query accepted")
		}
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = st.RecentMetricSamples(cancelled, "own", 10); err == nil {
		t.Fatal("cancel ignored")
	}
	if _, err = st.DB().Exec(`INSERT INTO metrics(agent_id,action_id,measured_at) VALUES('own',1,'invalid')`); err != nil {
		t.Fatal(err)
	}
	if _, err = st.RecentMetricSamples(ctx, "own", 10); err == nil {
		t.Fatal("invalid timestamp accepted")
	}
}
