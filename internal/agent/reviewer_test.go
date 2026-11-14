package agent_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
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
	tmpl.Writer = config.LLMConfig{Protocol: "openai", BaseURL: def.URL, Model: "luna", APIKey: config.NewSecret("test-secret-key"), PricePerMTokInUSD: 1, PricePerMTokOutUSD: 1}
	tmpl.PostWriter = config.LLMConfig{Protocol: "openai", BaseURL: post.URL, Model: "sol", APIKey: config.NewSecret("test-secret-key"), PricePerMTokInUSD: 1, PricePerMTokOutUSD: 1}
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

func TestWriterGetsTheBestAndWorstEpisodesOfItsKind(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var prompt atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct{ Content string } `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		prompt.Store(body.Messages[len(body.Messages)-1].Content)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"{\"texts\":[\"hello\"]}"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`))
	}))
	defer srv.Close()
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	tmpl := agent.OfflineTemplate()
	tmpl.Writer = config.LLMConfig{Protocol: "openai", BaseURL: srv.URL, Model: "luna", APIKey: config.NewSecret("test-secret-key"), PricePerMTokInUSD: 1, PricePerMTokOutUSD: 1}
	if err = st.EnsureAgent(ctx, tmpl.ID); err != nil {
		t.Fatal(err)
	}
	for i := int64(0); i < 9; i++ {
		seedEpisode(t, st, tmpl.ID, "reply", fmt.Sprintf("reply number %d", i), now.Add(-time.Duration(10+i)*time.Hour), 6, 100, i, 0)
		seedEpisode(t, st, tmpl.ID, "post", fmt.Sprintf("POST number %d", i), now.Add(-time.Duration(10+i)*time.Hour), 6, 100, i, 0)
	}
	wakeID, err := st.BeginWake(ctx, tmpl.ID, now, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	rv := agent.NewReviewer(agent.ReviewerOptions{Store: st, Template: tmpl, Now: func() time.Time { return now }, HTTPClient: &http.Client{}, Redactor: config.NewRedactor(tmpl)}, wakeID)
	if _, err = rv.Writer(ctx, quality.WriterInput{Kind: quality.Reply, Angle: "a", Fact: "f", Parent: "p"}); err != nil {
		t.Fatal(err)
	}
	got := prompt.Load().(string)
	for _, want := range []string{"untrusted data about past work", "- top: 8 likes", "- bottom: 0 likes", "reply number 8"} {
		if !strings.Contains(got, want) {
			t.Errorf("writer prompt lacks %q in\n%s", want, got)
		}
	}
	if strings.Contains(got, "POST number") {
		t.Error("a reply's writer sees post examples")
	}
}
