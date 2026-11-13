package agent_test

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/agent"
	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/llm"
	"github.com/canhta/threadz/internal/logging"
	"github.com/canhta/threadz/internal/store"
)

func embeddingRuntime(t *testing.T, calls ...llm.ToolCall) (*agent.Runtime, *store.Store, *bytes.Buffer) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "embed.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	fixture := agent.NewOfflineFixture(func() time.Time { return now })
	fixture.Calls = calls
	tmpl := agent.OfflineTemplate()
	var logs bytes.Buffer
	logger, _ := logging.New(&logs, "debug", "json")
	r, err := agent.New(agent.Options{Store: st, Template: tmpl, Logger: logger, Now: func() time.Time { return now }, HTTPClient: fixture.Client(), FetchTrends: fixture.Trends, FetchStudy: fixture.Study, EmbeddingBaseURL: "https://fixture.invalid"})
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	return r, st, &logs
}

func TestWake_EmbedsItsWritesAndSearchesByVector(t *testing.T) {
	r, st, _ := embeddingRuntime(t,
		agent.SyntheticCall("note_write", `{"topic":"t","text":"harbor notes"}`),
		agent.SyntheticCall("note_search", `{"query":"harbor"}`))
	if wake, err := r.Wake(context.Background()); err != nil || wake.EndReason != "completed" {
		t.Fatalf("%+v %v", wake, err)
	}
	var vectors int
	st.DB().QueryRow(`SELECT COUNT(*) FROM embeddings`).Scan(&vectors)
	usage, _ := st.LLMUsageForDay(context.Background(), "synthetic-offline", "2026-01-01")
	// The chat fixture bills 10 input tokens per request; the two embedding requests add one each.
	if vectors != 1 || usage.TokensIn%10 != 2 {
		t.Fatalf("vectors = %d, tokens in = %d; want the note embedded and two embedding requests billed", vectors, usage.TokensIn)
	}
}

func TestBackfillEmbeddings_EmbedsExistingRowsOnceUnderTheAgentPhase(t *testing.T) {
	r, st, logs := embeddingRuntime(t)
	ctx := context.Background()
	for _, topic := range []string{"a", "b"} {
		if _, err := st.WriteTopic(ctx, "synthetic-offline", topic, "", "old note "+topic, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.BackfillEmbeddings(ctx); err != nil {
		t.Fatal(err)
	}
	if err := r.BackfillEmbeddings(ctx); err != nil {
		t.Fatal(err)
	}
	var vectors int
	st.DB().QueryRow(`SELECT COUNT(*) FROM embeddings`).Scan(&vectors)
	phase, _ := st.LLMUsageForDayPhases(ctx, "synthetic-offline", "2026-01-01", "agent")
	if vectors != 2 || phase.TokensIn != 2 {
		t.Fatalf("vectors = %d, agent tokens = %d; want 2 and 2 (second call embeds nothing)", vectors, phase.TokensIn)
	}
	if !strings.Contains(logs.String(), logging.EmbeddingBackfilled) {
		t.Fatalf("backfill not logged:\n%s", logs.String())
	}
}

func TestRuntimeRejectsAnInvalidEmbeddingEndpoint(t *testing.T) {
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "bad.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	tmpl := agent.OfflineTemplate()
	tmpl.EmbeddingAPIKey = config.NewSecret("k")
	if _, err = agent.New(agent.Options{Store: st, Template: tmpl, EmbeddingBaseURL: "ftp://nowhere"}); err == nil {
		t.Fatal("invalid endpoint accepted")
	}
	tmpl.EmbeddingAPIKey = config.Secret{}
	if _, err = agent.New(agent.Options{Store: st, Template: tmpl}); err == nil {
		t.Fatal("runtime built without an embedding key")
	}
}
