package dashboard_test

import (
	"strings"
	"testing"

	"github.com/canhta/threadz/internal/store"
)

func TestRunsExpandOnlyForFailures(t *testing.T) {
	snap := started()
	snap.Wakes = []store.DashboardWake{finishedWake(1, "agent", "completed", 2), finishedWake(2, "agent", "completed", 1)}
	snap.ToolCalls = []store.DashboardToolCall{
		call(1, 1, "search", `{"outcome":"success"}`, ""),
		call(2, 1, "search", `{"outcome":"error","reason":"timeout"}`, ""),
		call(3, 2, "search", `{"outcome":"success"}`, ""),
	}
	page := mustPage(t, newHandler(newSource(snap)))
	if n := len(page.Wakes.Rows[0].ToolSummary) + len(page.Wakes.Rows[1].ToolSummary); n != 1 {
		t.Fatalf("only the failed step is listed: %+v", page.Wakes.Rows)
	}
	body := get(newHandler(newSource(snap)), "/").Body.String()
	if strings.Count(body, "<summary class=\"run\">") != 1 {
		t.Fatal("a run without failures must not expand")
	}
	for _, gone := range []string{"Per run", "Per engagement", "to date", "Next:", "±"} {
		if strings.Contains(body, gone) {
			t.Fatalf("page still has %q", gone)
		}
	}
}
