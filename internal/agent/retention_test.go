package agent_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/agent"
	"github.com/canhta/threadz/internal/store"
)

func TestSchedulerPrunesAtStartupAndOncePerDay(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	tmpl := agent.OfflineTemplate()
	if err = st.EnsureAgent(ctx, tmpl.ID); err != nil {
		t.Fatal(err)
	}
	if err = st.SetNextWake(ctx, tmpl.ID, now.Add(1000*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	old := now.Add(-91 * 24 * time.Hour).Format(time.RFC3339Nano)
	addOld := func() {
		if _, err := st.DB().Exec(`INSERT INTO owner_msgs(agent_id,direction,text,at) VALUES(?,'in','x',?)`, tmpl.ID, old); err != nil {
			t.Fatal(err)
		}
	}
	rows := func() int {
		var n int
		if err := st.DB().QueryRow(`SELECT COUNT(*) FROM owner_msgs`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	addOld()
	r, err := agent.New(agent.Options{Store: st, Template: tmpl, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	stop := errors.New("stop")
	step := 0
	wait := func(context.Context, time.Duration) error {
		step++
		switch step {
		case 1:
			if rows() != 0 {
				t.Error("startup did not prune")
			}
			addOld()
			now = now.Add(time.Hour)
		case 2:
			if rows() != 1 {
				t.Error("pruned twice on the same day")
			}
			now = now.Add(24 * time.Hour)
		default:
			if rows() != 0 {
				t.Error("next day did not prune")
			}
			return stop
		}
		return nil
	}
	err = (agent.Scheduler{Agents: []*agent.Runtime{r}, Now: func() time.Time { return now }, Wait: wait}).Run(ctx, false)
	if !errors.Is(err, stop) || step != 3 {
		t.Fatalf("err=%v steps=%d", err, step)
	}
}
