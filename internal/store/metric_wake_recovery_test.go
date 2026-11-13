package store_test

import (
	"context"
	"github.com/canhta/threadz/internal/store"
	"strings"
	"testing"
	"time"
)

func TestInterruptedMetricWakePreservesAgentScheduleAndReadRetry(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err = st.EnsureAgent(ctx, "own"); err != nil {
		t.Fatal(err)
	}
	if err = st.SetNextWake(ctx, "own", now.Add(15*time.Minute)); err != nil {
		t.Fatal(err)
	}
	id, err := st.BeginMetricWake(ctx, "own", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.DB().Exec(`INSERT INTO tool_calls(agent_id,wake_id,tool,args_json,at) VALUES('own',?,'read_own_metrics','{}',?)`, id, now.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	ids, err := st.RecoverWakes(ctx, "own", now.Add(time.Minute), now.Add(time.Hour))
	if err != nil || len(ids) != 1 {
		t.Fatalf("ids=%v err=%v", ids, err)
	}
	state, err := st.AgentState(ctx, "own")
	if err != nil || !state.NextWake.Equal(now.Add(15*time.Minute)) {
		t.Fatalf("schedule changed: %+v %v", state, err)
	}
	var audit string
	if err = st.DB().QueryRow(`SELECT result_json FROM tool_calls WHERE wake_id=?`, id).Scan(&audit); err != nil || !strings.Contains(audit, `"outcome":"interrupted_read"`) || strings.Contains(audit, "assumed_sent") {
		t.Fatalf("audit=%s err=%v", audit, err)
	}
}
