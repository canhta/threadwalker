package llm_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/llm"
	"github.com/canhta/threadz/internal/logging"
	"github.com/canhta/threadz/internal/store"
)

const embedKey = "sk-embedding-secret-value"

var embedNow = time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)

// fakeEmbeddings answers /embeddings with one vector per input, out of order,
// and counts the inputs it was asked to embed.
type fakeEmbeddings struct {
	mu     sync.Mutex
	inputs [][]string
	auth   string
	body   map[string]any
	status int
	mangle func(map[string]any)
	tokens int
}

func (f *fakeEmbeddings) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.auth = r.Header.Get("Authorization")
	f.body = nil
	_ = json.NewDecoder(r.Body).Decode(&f.body)
	if f.status != 0 {
		http.Error(w, "nope "+embedKey, f.status)
		return
	}
	var texts []string
	for _, v := range f.body["input"].([]any) {
		texts = append(texts, v.(string))
	}
	f.inputs = append(f.inputs, texts)
	data := make([]map[string]any, len(texts))
	for i := range texts {
		vec := make([]float32, llm.EmbeddingDimensions)
		vec[i%len(vec)] = 1
		data[len(texts)-1-i] = map[string]any{"index": i, "embedding": vec}
	}
	out := map[string]any{"data": data, "usage": map[string]any{"total_tokens": f.tokens}}
	if f.mangle != nil {
		f.mangle(out)
	}
	_ = json.NewEncoder(w).Encode(out)
}

func (f *fakeEmbeddings) requests() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.inputs) }

type embedEnv struct {
	st     *store.Store
	fake   *fakeEmbeddings
	srv    *httptest.Server
	logs   *bytes.Buffer
	limits config.LimitsConfig
}

func newEmbedEnv(t *testing.T) *embedEnv {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "e.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.EnsureAgent(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	fake := &fakeEmbeddings{tokens: 1000}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	return &embedEnv{st: st, fake: fake, srv: srv, logs: &bytes.Buffer{}, limits: config.LimitsConfig{MaxUSDPerDay: 1}}
}

func (e *embedEnv) embedder(t *testing.T, opts llm.Options) *llm.Embedder {
	t.Helper()
	opts.Now = func() time.Time { return embedNow }
	opts.HTTPClient = e.srv.Client()
	opts.Logger = slog.New(slog.NewJSONHandler(e.logs, nil))
	emb, err := llm.NewEmbedder(e.srv.URL, config.NewSecret(embedKey), e.limits, e.st, opts)
	if err != nil {
		t.Fatal(err)
	}
	return emb
}

func (e *embedEnv) wake(t *testing.T, kind string) int64 {
	t.Helper()
	res, err := e.st.DB().Exec(`INSERT INTO wakes(agent_id,kind,started_at) VALUES('a',?,?)`, kind, embedNow.Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func TestEmbed_ReturnsVectorsInInputOrderAndBillsTheWakePhase(t *testing.T) {
	env := newEmbedEnv(t)
	ctx := context.Background()
	vecs, err := env.embedder(t, llm.Options{Phase: "dream", CapUSD: 1}).Embed(ctx, "a", env.wake(t, "dream"), []string{"x", "y"})
	if err != nil || len(vecs) != 2 || vecs[0][0] != 1 || vecs[1][1] != 1 {
		t.Fatalf("Embed = %v, %v; want vectors ordered by input index", len(vecs), err)
	}
	if env.fake.auth != "Bearer "+embedKey || env.fake.body["model"] != llm.EmbeddingModel {
		t.Fatalf("auth %q body %v", env.fake.auth, env.fake.body)
	}
	if _, set := env.fake.body["dimensions"]; set {
		t.Fatal("dimensions sent; full size is the default")
	}
	dream, _ := env.st.LLMUsageForDayPhases(ctx, "a", "2026-11-12", "dream")
	total, _ := env.st.LLMUsageForDay(ctx, "a", "2026-11-12")
	if dream.TokensIn != 1000 || math.Abs(dream.CostUSD-0.00013) > 1e-12 || total != dream {
		t.Fatalf("dream ledger %+v, total %+v; want 1000 tokens at $0.13/M", dream, total)
	}
}

func TestEmbed_WithoutAWakeBillsTheAgentPhase(t *testing.T) {
	env := newEmbedEnv(t)
	ctx := context.Background()
	if _, err := env.embedder(t, llm.Options{}).Embed(ctx, "a", 0, []string{"x"}); err != nil {
		t.Fatal(err)
	}
	agent, _ := env.st.LLMUsageForDayPhases(ctx, "a", "2026-11-12", "agent")
	if agent.TokensIn != 1000 {
		t.Fatalf("agent phase = %+v", agent)
	}
}

func TestEmbed_StopsAtTheDailyCapWithoutARequest(t *testing.T) {
	env := newEmbedEnv(t)
	ctx := context.Background()
	if err := env.st.RecordUnattributedLLMUsage(ctx, "a", "2026-11-12", store.LLMUsage{CostUSD: 1}); err != nil {
		t.Fatal(err)
	}
	_, err := env.embedder(t, llm.Options{}).Embed(ctx, "a", 0, []string{"x"})
	if !errors.Is(err, llm.ErrBudget) || env.fake.requests() != 0 {
		t.Fatalf("err = %v, requests = %d; want ErrBudget and no request", err, env.fake.requests())
	}
	if got := llm.EmbeddingFailureReason(err); got != "budget_limit" {
		t.Fatalf("reason = %q", got)
	}
}

func TestEmbed_FailuresCarryAReasonAndNeverTheKey(t *testing.T) {
	cases := []struct {
		name   string
		setup  func(*embedEnv)
		reason string
	}{
		{"http error", func(e *embedEnv) { e.fake.status = http.StatusUnauthorized }, "embedding_http_error"},
		{"count mismatch", func(e *embedEnv) {
			e.fake.mangle = func(m map[string]any) { m["data"] = []any{} }
		}, "embedding_invalid_response"},
		{"wrong dimensions", func(e *embedEnv) {
			e.fake.mangle = func(m map[string]any) { m["data"] = []any{map[string]any{"index": 0, "embedding": []float32{1}}} }
		}, "embedding_invalid_response"},
		{"duplicate index", func(e *embedEnv) {
			e.fake.mangle = func(m map[string]any) {
				d := m["data"].([]map[string]any)
				m["data"] = []map[string]any{d[0], d[0]}
			}
		}, "embedding_invalid_response"},
		{"negative tokens", func(e *embedEnv) { e.fake.tokens = -1 }, "embedding_invalid_response"},
		{"not json", func(e *embedEnv) {
			e.fake.mangle = func(m map[string]any) { m["data"] = "garbage" }
		}, "embedding_invalid_response"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newEmbedEnv(t)
			tc.setup(env)
			texts := []string{"x"}
			if tc.name == "duplicate index" {
				texts = []string{"x", "y"}
			}
			_, err := env.embedder(t, llm.Options{}).Embed(context.Background(), "a", 0, texts)
			if err == nil || llm.EmbeddingFailureReason(err) != tc.reason || strings.Contains(err.Error(), embedKey) {
				t.Fatalf("err = %v reason %q; want %q without the key", err, llm.EmbeddingFailureReason(err), tc.reason)
			}
			if u, _ := env.st.LLMUsageForDay(context.Background(), "a", "2026-11-12"); u.TokensIn != 0 {
				t.Fatalf("failed request billed: %+v", u)
			}
		})
	}
}

func TestEmbed_TransportAndTimeoutReasons(t *testing.T) {
	env := newEmbedEnv(t)
	env.srv.Close()
	_, err := env.embedder(t, llm.Options{}).Embed(context.Background(), "a", 0, []string{"x"})
	if got := llm.EmbeddingFailureReason(err); got != "embedding_transport_error" || strings.Contains(err.Error(), embedKey) {
		t.Fatalf("err = %v reason %q", err, got)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := env.embedder(t, llm.Options{}).Embed(cancelled, "a", 0, []string{"x"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled err = %v", err)
	}
}

func TestEmbed_UsageWriteFailureIsReported(t *testing.T) {
	env := newEmbedEnv(t)
	_, err := env.embedder(t, llm.Options{}).Embed(context.Background(), "a", 999, []string{"x"})
	if got := llm.EmbeddingFailureReason(err); got != "embedding_accounting_failed" {
		t.Fatalf("err = %v reason %q; want accounting failure for an unknown wake", err, got)
	}
}

func TestNewEmbedder_RejectsBadConfiguration(t *testing.T) {
	env := newEmbedEnv(t)
	good := llm.Options{}
	for name, call := range map[string]func() error{
		"bad url": func() error {
			_, e := llm.NewEmbedder("ftp://x", config.NewSecret("k"), env.limits, env.st, good)
			return e
		},
		"no key": func() error {
			_, e := llm.NewEmbedder(env.srv.URL, config.Secret{}, env.limits, env.st, good)
			return e
		},
		"no store": func() error {
			_, e := llm.NewEmbedder(env.srv.URL, config.NewSecret("k"), env.limits, nil, good)
			return e
		},
		"unknown phase": func() error {
			_, e := llm.NewEmbedder(env.srv.URL, config.NewSecret("k"), env.limits, env.st, llm.Options{Phase: "x"})
			return e
		},
	} {
		if call() == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestSync_EmbedsEachTextOnceAndOnlyWhenItChanges(t *testing.T) {
	env := newEmbedEnv(t)
	ctx := context.Background()
	emb := env.embedder(t, llm.Options{})
	for _, topic := range []string{"one", "two"} {
		if _, err := env.st.WriteTopic(ctx, "a", topic, "", "same text", embedNow); err != nil {
			t.Fatal(err)
		}
	}
	env.st.WriteTopic(ctx, "a", "three", "", "other text", embedNow)
	env.st.DB().Exec(`INSERT INTO actions(agent_id,kind,text,posted_at) VALUES('a','post','same text',?)`, embedNow.Format(time.RFC3339Nano))

	wake := env.wake(t, "agent")
	n, err := emb.Sync(ctx, "a", wake)
	if err != nil || n != 4 {
		t.Fatalf("Sync = %d, %v; want 4 rows", n, err)
	}
	if len(env.fake.inputs) != 1 || strings.Join(env.fake.inputs[0], "|") != "same text|other text" {
		t.Fatalf("requests %v; want one request with each distinct text once", env.fake.inputs)
	}
	if n, err := emb.Sync(ctx, "a", wake); n != 0 || err != nil || env.fake.requests() != 1 {
		t.Fatalf("second Sync = %d, %v, %d requests; want none", n, err, env.fake.requests())
	}

	env.st.WriteTopic(ctx, "a", "one", "new description", "same text", embedNow)
	emb.Sync(ctx, "a", wake)
	if env.fake.requests() != 1 {
		t.Fatalf("unchanged text re-embedded: %d requests", env.fake.requests())
	}
	env.st.WriteTopic(ctx, "a", "one", "", "changed text", embedNow)
	if n, _ := emb.Sync(ctx, "a", wake); n != 1 || env.fake.requests() != 2 || env.fake.inputs[1][0] != "changed text" {
		t.Fatalf("changed text: n=%d inputs=%v; want exactly one new embed", n, env.fake.inputs)
	}
}

func TestSync_FailureLeavesRowsPendingForTheNextCall(t *testing.T) {
	env := newEmbedEnv(t)
	ctx := context.Background()
	emb := env.embedder(t, llm.Options{})
	env.st.WriteTopic(ctx, "a", "t", "", "text", embedNow)
	env.fake.status = http.StatusInternalServerError
	if n, err := emb.Sync(ctx, "a", 0); err == nil || n != 0 {
		t.Fatalf("Sync = %d, %v; want a failure", n, err)
	}
	env.fake.status = 0
	if n, err := emb.Sync(ctx, "a", 0); err != nil || n != 1 {
		t.Fatalf("retry = %d, %v; want the row embedded", n, err)
	}
}

func TestSync_BatchesLargeBacklogs(t *testing.T) {
	env := newEmbedEnv(t)
	ctx := context.Background()
	for i := 0; i < 70; i++ {
		env.st.WriteTopic(ctx, "a", fmt.Sprintf("t%d", i), "", fmt.Sprintf("text %d", i), embedNow)
	}
	if n, err := env.embedder(t, llm.Options{}).Sync(ctx, "a", 0); err != nil || n != 70 || env.fake.requests() != 2 {
		t.Fatalf("Sync = %d, %v, %d requests; want 70 rows in 2 requests", n, err, env.fake.requests())
	}
}

func TestSync_LogsReasonCodesWithoutKeyOrText(t *testing.T) {
	env := newEmbedEnv(t)
	ctx := context.Background()
	emb := env.embedder(t, llm.Options{})
	env.st.WriteTopic(ctx, "a", "t", "", "private note text", embedNow)
	env.fake.status = http.StatusUnauthorized
	emb.Sync(ctx, "a", 0)
	env.fake.status = 0
	emb.Sync(ctx, "a", 0)
	out := env.logs.String()
	for _, want := range []string{logging.EmbeddingBackfilled, `"reason":"embedding_http_error"`, `"count":1`} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %s:\n%s", want, out)
		}
	}
	wake := env.wake(t, "agent")
	env.st.WriteTopic(ctx, "a", "u", "", "more text", embedNow)
	emb.Sync(ctx, "a", wake)
	if !strings.Contains(env.logs.String(), logging.EmbeddingSynced) {
		t.Error("wake sync not logged as embedding.synced")
	}
	if all := env.logs.String(); strings.Contains(all, embedKey) || strings.Contains(all, "private note text") {
		t.Fatalf("log leaks key or text:\n%s", all)
	}
}
