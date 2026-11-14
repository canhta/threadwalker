package agent_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/agent"
	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/llm"
	"github.com/canhta/threadz/internal/store"
	"github.com/canhta/threadz/internal/threads"
)

func transcriptRuntime(t *testing.T, calls []llm.ToolCall, redactor *config.Redactor) (*agent.Runtime, *store.Store, string) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "transcript.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	tmpl := agent.OfflineTemplate()
	fixture := agent.NewOfflineFixture(func() time.Time { return now })
	fixture.Calls = calls
	factory := func(context.Context, config.Template) (threads.Backend, error) {
		m := threads.NewMemory()
		m.Pages[threads.OpReadFeed] = threads.Page{Kind: "feed", Posts: []threads.Post{{Text: strings.Repeat("é", llm.MaxTranscriptToolBytes)}}}
		return m, nil
	}
	r, err := agent.New(agent.Options{Store: st, Template: tmpl, Redactor: redactor, Now: func() time.Time { return now }, HTTPClient: fixture.Client(), FetchTrends: fixture.Trends, ThreadsFactory: factory})
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Start(ctx); err != nil {
		t.Fatal(err)
	}
	return r, st, tmpl.ID
}

func TestWakeStoresRedactedBoundedTranscript(t *testing.T) {
	const secret = "TOPSECRET-value"
	big := strings.Repeat("é", llm.MaxTranscriptToolBytes)
	calls := []llm.ToolCall{
		agent.SyntheticCall("find_posts", `{"source":"search","query":"`+secret+`"}`),
		agent.SyntheticCall("find_posts", `{"source":"search","query":"`+big+`"}`),
		agent.SyntheticCall("find_posts", `{"source":"feed"}`),
		agent.SyntheticCall("read_replies", `{}`),
	}
	r, st, id := transcriptRuntime(t, calls, config.NewRedactor(config.NewSecret(secret)))
	wake, err := r.Wake(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	entries, err := st.Transcript(context.Background(), id, wake.ID)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	clipped := false
	for _, e := range entries {
		kinds = append(kinds, e.Kind)
		if strings.Contains(e.Body, secret) {
			t.Fatalf("secret in %s entry: %s", e.Kind, e.Body)
		}
		if e.Kind == store.TranscriptAssistant && len(e.Body) > 3*llm.MaxTranscriptToolBytes {
			t.Fatalf("assistant entry unbounded: %d", len(e.Body))
		}
		if e.Kind == store.TranscriptToolResult {
			var body struct {
				Truncated bool
				Untrusted bool
				Tool      string
				Output    string
			}
			if err := json.Unmarshal([]byte(e.Body), &body); err != nil || len(body.Output) > llm.MaxTranscriptToolBytes {
				t.Fatalf("tool result not capped: %d err=%v", len(body.Output), err)
			}
			// The dispatcher clips a result far below the transcript cap, dropping whole items.
			clipped = clipped || strings.Contains(body.Output, `"clipped_lists"`)
			if !body.Untrusted {
				t.Fatalf("%s untrusted=%v", body.Tool, body.Untrusted)
			}
		}
	}
	if kinds[0] != store.TranscriptContext || kinds[len(kinds)-1] != store.TranscriptEnd {
		t.Fatalf("kinds=%v", kinds)
	}
	want := map[string]int{store.TranscriptAssistant: 2, store.TranscriptToolResult: 4, store.TranscriptUsage: 2}
	for kind, n := range want {
		got := 0
		for _, k := range kinds {
			if k == kind {
				got++
			}
		}
		if got != n {
			t.Fatalf("%s entries=%d want %d (kinds=%v)", kind, got, n, kinds)
		}
	}
	if !clipped {
		t.Fatal("oversized tool result was not clipped")
	}
	var end struct {
		Reason string
		Steps  int
	}
	if err := json.Unmarshal([]byte(entries[len(entries)-1].Body), &end); err != nil || end.Reason != "completed" || end.Steps != wake.Steps {
		t.Fatalf("end=%+v err=%v", end, err)
	}
}
