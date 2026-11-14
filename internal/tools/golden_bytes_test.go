package tools_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/guard"
	"github.com/canhta/threadz/internal/llm"
	"github.com/canhta/threadz/internal/quality"
	"github.com/canhta/threadz/internal/store"
	"github.com/canhta/threadz/internal/tools"
	"github.com/canhta/threadz/internal/trends"
)

// MaxToolDefinitionBytes is the budget for every tool definition an agent wake
// sends with each model request: descriptions say what a tool does, argument
// descriptions live in the schema, and policy lives in guard code and its hints.
const (
	maxToolDefinitionBytes = 6400
	maxDescriptionBytes    = 300
)

// agentDefinitions is what an agent wake offers when everything is configured:
// web search, owner messages and ideas.
func agentDefinitions(t *testing.T) []llm.Tool {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "golden.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	now := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	tmpl := config.Template{ID: "agent", DryRun: true, Limits: config.LimitsConfig{PostsPerDay: 3, RepliesPerDay: 5, RepliesPerUser7d: 1, MinSecondsBetweenActions: 1, MaxStepsPerWake: 10, MaxUSDPerDay: 1, MinMinutesBetweenWakes: 15}}
	tmpl.WebSearch.Enabled = true
	if err = st.EnsureAgent(ctx, tmpl.ID); err != nil {
		t.Fatal(err)
	}
	wakeID, err := st.BeginWake(ctx, tmpl.ID, now, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	d, err := tools.NewDispatcher(tools.Options{Scope: guard.Scope{Store: st, Template: tmpl, WakeID: wakeID, Now: func() time.Time { return now },
		Ideas: func(context.Context, []quality.Item) ([]quality.Idea, error) { return nil, nil }}, Owner: fakeOwner{}, SearchWeb: trends.NewWebSearcher(http.DefaultClient), FetchTrends: func(context.Context) ([]trends.Trend, error) { return nil, nil }})
	if err != nil {
		t.Fatal(err)
	}
	return d.Definitions()
}

// The tool definitions are pinned byte for byte: a change to a description, an
// argument or its description shows up in testdata/agent-tools.golden.json for
// review, and the total stays within the budget.
func TestAgentToolDefinitionsGolden(t *testing.T) {
	defs := agentDefinitions(t)
	total := 0
	for _, def := range defs {
		raw, _ := json.Marshal(def)
		total += len(raw)
		if n := len(def.Function.Description); n > maxDescriptionBytes {
			t.Errorf("%s: description is %d bytes, over %d", def.Function.Name, n, maxDescriptionBytes)
		}
	}
	if total > maxToolDefinitionBytes {
		t.Errorf("tool definitions are %d bytes, over the %d byte budget", total, maxToolDefinitionBytes)
	}
	got, err := json.MarshalIndent(defs, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join("testdata", "agent-tools.golden.json")
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err = os.WriteFile(path, append(got, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != string(got)+"\n" {
		t.Fatalf("tool definitions differ from %s; rerun with UPDATE_GOLDEN=1 after reviewing (total %d bytes)", path, total)
	}
	t.Logf("agent tool definitions: %d bytes in %d tools", total, len(defs))
}

// Every tool of every phase keeps its description short: what it does, not the
// policy, which the guard enforces and states in its refusal hints.
func TestEverySpecDescriptionIsShort(t *testing.T) {
	for _, spec := range guard.Specs() {
		if n := len(spec.Description); n > maxDescriptionBytes {
			t.Errorf("%s: description is %d bytes, over %d", spec.Name, n, maxDescriptionBytes)
		}
		for _, a := range spec.Args {
			if a.Desc == "" && !(a.Name == "text" && spec.Name != "lesson_add") {
				t.Errorf("%s.%s has no description in the schema", spec.Name, a.Name)
			}
		}
	}
}
