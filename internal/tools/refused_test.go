package tools_test

import (
	"strings"
	"testing"

	"github.com/canhta/threadz/internal/threads"
	"github.com/canhta/threadz/internal/tools"
)

func TestLimitedAccessReadIsRecordedAndHiddenNextWake(t *testing.T) {
	st, opts, _ := setup(t)
	memory := threads.NewMemory()
	memory.Pages[threads.OpSearch] = threads.Page{Kind: "search", LimitedAccess: "No results."}
	opts.Threads = memory
	d, err := tools.NewDispatcher(opts)
	if err != nil {
		t.Fatal(err)
	}
	dispatch(t, d, "search", `{"query":"agents"}`)
	hidden, err := st.RefusedTools(t.Context(), "agent", opts.Now())
	if err != nil || len(hidden) != 1 || hidden[0] != "search" {
		t.Fatalf("hidden=%v err=%v", hidden, err)
	}
	opts.Hidden = hidden
	next, err := tools.NewDispatcher(opts)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, def := range next.Definitions() {
		names = append(names, def.Function.Name)
	}
	if got := " " + strings.Join(names, " ") + " "; strings.Contains(got, " search ") || !strings.Contains(got, " profile_posts ") {
		t.Fatalf("tools=%s", got)
	}
}
