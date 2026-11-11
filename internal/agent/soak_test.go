package agent_test

import (
	"context"
	"github.com/canhta/threadwalker/internal/agent"
	"path/filepath"
	"testing"
)

func TestOfflineSoak48LogicalHoursWithRestart(t *testing.T) {
	evidence, err := agent.RunOfflineSoak(context.Background(), filepath.Join(t.TempDir(), "soak.db"), 48, nil)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.TimingKind != "logical_time" || evidence.LogicalHours != 48 || evidence.CompletedWakes != 48 || evidence.InterruptedWakes != 1 || evidence.DryRunActions != 49 || evidence.Restarts != 1 {
		t.Fatalf("%+v", evidence)
	}
	if len(evidence.Wakes) != 49 {
		t.Fatalf("wakes=%d", len(evidence.Wakes))
	}
}
