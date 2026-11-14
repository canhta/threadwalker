package agent_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/agent"
	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/store"
)

func TestSilentAgentIsReportedOncePerLocalDayInWakingHours(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name   string
		silent time.Duration // how long before the check the last publish was
		quiet  bool
		paused bool
		want   int
	}{
		{"past the limit and the slack", 12 * time.Hour, false, false, 1},
		{"inside the slack", 10 * time.Hour, false, false, 0},
		{"quiet hours", 12 * time.Hour, true, false, 0},
		{"paused", 12 * time.Hour, false, true, 0},
	} {
		st, err := store.Open(ctx, filepath.Join(t.TempDir(), "silent.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { st.Close() })
		tmpl := agent.OfflineTemplate()
		tmpl.KPI = config.KPIConfig{EngagementsPerDay: 10, MaxHoursWithoutPublish: 8, OnTargetMinutes: 180}
		if tc.quiet {
			tmpl.Schedule.QuietFrom, tmpl.Schedule.QuietUntil = "18:00", "22:00"
		}
		if err = st.EnsureAgent(ctx, tmpl.ID); err != nil {
			t.Fatal(err)
		}
		if _, err = st.SeedAction(ctx, store.SeededAction{AgentID: tmpl.ID, Kind: "post", Text: "seed", DryRun: tmpl.DryRun, PostedAt: kpiNow.Add(-tc.silent)}); err != nil {
			t.Fatal(err)
		}
		if tc.paused {
			if _, err = st.DB().Exec(`UPDATE agents SET status='paused'`); err != nil {
				t.Fatal(err)
			}
		}
		r, err := agent.New(agent.Options{Store: st, Template: tmpl, Now: func() time.Time { return kpiNow }})
		if err != nil {
			t.Fatal(err)
		}
		notices := func() (n int) {
			if err := st.DB().QueryRow(`SELECT COUNT(*) FROM owner_msgs WHERE kind='notice' AND event_key LIKE 'notice:silent_agent:%'`).Scan(&n); err != nil {
				t.Fatal(err)
			}
			return n
		}
		for _, at := range []time.Time{kpiNow, kpiNow.Add(time.Hour)} {
			if err = r.WarnIfSilent(ctx, at); err != nil {
				t.Fatal(err)
			}
		}
		if got := notices(); got != tc.want {
			t.Errorf("%s: %d notices, want %d", tc.name, got, tc.want)
		}
		if tc.want == 0 {
			continue
		}
		if err = r.WarnIfSilent(ctx, kpiNow.Add(24*time.Hour)); err != nil || notices() != 2 {
			t.Errorf("%s: the next local day must notify again: %d err %v", tc.name, notices(), err)
		}
		var text string
		if err = st.DB().QueryRow(`SELECT text FROM owner_msgs WHERE event_key LIKE 'notice:silent_agent:%' ORDER BY id LIMIT 1`).Scan(&text); err != nil || text != "The agent has published nothing for 12 hours, longer than the 8 hours its targets allow. It is running; send /status to see its next run and how its latest runs ended." {
			t.Errorf("%s: text %q err %v", tc.name, text, err)
		}
	}
}
