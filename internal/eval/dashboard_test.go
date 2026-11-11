package eval

import (
	"context"
	"testing"
	"time"
)

func TestDashboardCasesExercisePublicProjectionAndReadOnlySQLite(t *testing.T) {
	for _, scenario := range []string{"privacy", "read_only", "query_failure"} {
		t.Run(scenario, func(t *testing.T) {
			end := "completed"
			if scenario == "query_failure" {
				end = "persistence_error"
			}
			r := runDashboardCase(context.Background(), Case{ID: "dashboard_" + scenario, DashboardScenario: scenario, Expect: Expectation{EndReason: end, Tools: []string{}}}, Profile{ID: "synthetic"}, 0, Options{Now: func() time.Time { return time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC) }})
			if !r.Passed {
				t.Fatalf("actual dashboard case failed: %s", r.Reason)
			}
		})
	}
}
