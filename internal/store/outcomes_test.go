package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestMetricSamplesKeepAbsentCountersAbsent(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "absent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err = st.EnsureAgent(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	if _, err = st.db.Exec(`INSERT INTO actions(id,agent_id,kind,text,dry_run,posted_at,thread_url,media_id) VALUES(1,'a','reply','x',0,?,'https://x.invalid/1','1')`, formatTime(at)); err != nil {
		t.Fatal(err)
	}
	if _, err = st.SeedMetricJobs(ctx, "a", at.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	jobs, err := st.ClaimMetricJobs(ctx, "a", at.Add(2*time.Hour), time.Minute, 10)
	if err != nil || len(jobs) == 0 {
		t.Fatalf("jobs=%v err=%v", jobs, err)
	}
	two, replied := int64(2), true
	if ok, e := st.CompleteMetricJobs(ctx, jobs, at.Add(2*time.Hour), MetricValues{Views: 10, Quotes: &two, AuthorReplied: &replied}); e != nil || !ok {
		t.Fatalf("complete ok=%v err=%v", ok, e)
	}
	samples, err := st.RecentMetricSamples(ctx, "a", 5)
	if err != nil || len(samples) != 1 {
		t.Fatalf("samples=%v err=%v", samples, err)
	}
	s := samples[0]
	if s.Quotes == nil || *s.Quotes != 2 || s.Shares != nil || s.AuthorReplied == nil || !*s.AuthorReplied {
		t.Fatalf("sample=%+v: quotes and author reply recorded, shares absent", s)
	}
}
