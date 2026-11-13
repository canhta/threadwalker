package agent_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/agent"
	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/quality"
	"github.com/canhta/threadz/internal/store"
)

func TestPostsUseThePostWriterAndRepliesAndIdeasUseTheWriter(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "reviewer.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var defaultHits, postHits atomic.Int32
	fake := func(hits *atomic.Int32) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"{\"texts\":[\"hello\"]}"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`))
		}))
	}
	def, post := fake(&defaultHits), fake(&postHits)
	defer def.Close()
	defer post.Close()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tmpl := agent.OfflineTemplate()
	tmpl.Writer = config.LLMConfig{Protocol: "openai", BaseURL: def.URL, Model: "luna", APIKey: config.NewSecret("k"), PricePerMTokInUSD: 1, PricePerMTokOutUSD: 1}
	tmpl.PostWriter = config.LLMConfig{Protocol: "openai", BaseURL: post.URL, Model: "sol", APIKey: config.NewSecret("k"), PricePerMTokInUSD: 1, PricePerMTokOutUSD: 1}
	if err = st.EnsureAgent(ctx, tmpl.ID); err != nil {
		t.Fatal(err)
	}
	wakeID, err := st.BeginWake(ctx, tmpl.ID, now, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	rv := agent.NewReviewer(agent.ReviewerOptions{Store: st, Template: tmpl, Now: func() time.Time { return now }, HTTPClient: &http.Client{}, Redactor: config.NewRedactor(tmpl)}, wakeID)

	if _, err = rv.Writer(ctx, quality.WriterInput{Kind: quality.Post, Angle: "a", Fact: "f"}); err != nil {
		t.Fatal(err)
	}
	if defaultHits.Load() != 0 || postHits.Load() != 1 {
		t.Fatalf("post draft: default %d post %d", defaultHits.Load(), postHits.Load())
	}
	if _, err = rv.Writer(ctx, quality.WriterInput{Kind: quality.Reply, Angle: "a", Fact: "f", Parent: "p"}); err != nil {
		t.Fatal(err)
	}
	if defaultHits.Load() != 1 || postHits.Load() != 1 {
		t.Fatalf("reply draft: default %d post %d", defaultHits.Load(), postHits.Load())
	}
	_, _ = rv.Ideas(ctx, nil) // the fake answers with a writer body; only the routing matters
	if defaultHits.Load() != 2 || postHits.Load() != 1 {
		t.Fatalf("ideas: default %d post %d", defaultHits.Load(), postHits.Load())
	}
}
