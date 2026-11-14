package agent_test

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/agent"
	"github.com/canhta/threadz/internal/llm"
	"github.com/canhta/threadz/internal/store"
)

func TestDailyActionLimitStopsWakeBeforeFurtherTools(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "limits.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	tmpl := agent.OfflineTemplate()
	tmpl.Limits.PostsPerDay = 1
	fixture := agent.NewOfflineFixture(func() time.Time { return now })
	fixture.Calls = []llm.ToolCall{agent.SyntheticCall("post", `{"text":"synthetic cap boundary"}`), agent.SyntheticCall("post", `{"text":"synthetic refused boundary"}`), agent.SyntheticCall("fetch_trends", `{}`)}
	runtime, err := agent.New(agent.Options{Store: st, Template: tmpl, Now: func() time.Time { return now }, HTTPClient: fixture.Client(), FetchTrends: fixture.Trends})
	if err != nil {
		t.Fatal(err)
	}
	if err = runtime.Start(ctx); err != nil {
		t.Fatal(err)
	}
	wake, err := runtime.Wake(ctx)
	if err != nil || wake.EndReason != "action_limit" {
		t.Fatalf("wake %+v err %v", wake, err)
	}
	calls, err := st.ToolCallNames(ctx, tmpl.ID, wake.ID)
	if err != nil || slices.Contains(calls, "fetch_trends") {
		t.Fatalf("continued after cap %v %v", calls, err)
	}
}
