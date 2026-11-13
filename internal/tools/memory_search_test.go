package tools_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/guard"
	"github.com/canhta/threadz/internal/llm"
	"github.com/canhta/threadz/internal/store"
	"github.com/canhta/threadz/internal/tools"
)

const embeddingKeyCanary = "sk-embedding-canary-0123456789"

// wordVector places text on one axis per known word, so fake embeddings put
// texts that share a concept next to each other. Unknown text is the last axis.
func wordVector(axes map[string]int, text string) []float32 {
	vec := make([]float32, llm.EmbeddingDimensions)
	for word, axis := range axes {
		if strings.Contains(strings.ToLower(text), word) {
			vec[axis] = 1
		}
	}
	vec[llm.EmbeddingDimensions-1] = 0.01
	return vec
}

// embeddingsServer answers /embeddings with vecFor(text); a nil vector makes
// the whole request fail with 500.
func embeddingsServer(t *testing.T, vecFor func(string) []float32) (*httptest.Server, *[][]string) {
	t.Helper()
	var requests [][]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Input []string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		requests = append(requests, in.Input)
		data := make([]map[string]any, len(in.Input))
		for i, text := range in.Input {
			vec := vecFor(text)
			if vec == nil {
				http.Error(w, "unavailable", http.StatusInternalServerError)
				return
			}
			data[i] = map[string]any{"index": i, "embedding": vec}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data, "usage": map[string]int{"total_tokens": 10 * len(in.Input)}})
	}))
	t.Cleanup(srv.Close)
	return srv, &requests
}

type searchEnv struct {
	st   *store.Store
	d    *tools.Dispatcher
	logs *bytes.Buffer
	opts tools.Options
}

func newSearchEnv(t *testing.T, srv *httptest.Server, template func(*config.Template)) *searchEnv {
	t.Helper()
	st, opts, _ := setup(t)
	if template != nil {
		template(&opts.Template)
	}
	env := &searchEnv{st: st, logs: &bytes.Buffer{}}
	opts.Logger = slog.New(slog.NewJSONHandler(env.logs, nil))
	if srv != nil {
		emb, err := llm.NewEmbedder(srv.URL, config.NewSecret(embeddingKeyCanary), config.LimitsConfig{MaxUSDPerDay: 1}, st, llm.Options{Now: opts.Now, HTTPClient: srv.Client(), Logger: opts.Logger})
		if err != nil {
			t.Fatal(err)
		}
		opts.Embedder = emb
	}
	opts.Scope = guard.Scope{Store: st, Template: opts.Template, WakeID: 1, Now: opts.Now, Logger: opts.Logger}
	var err error
	if env.d, err = tools.NewDispatcher(opts); err != nil {
		t.Fatal(err)
	}
	env.opts = opts
	return env
}

func (e *searchEnv) search(t *testing.T, query string) []string {
	t.Helper()
	var hits []struct{ Ref string }
	if err := json.Unmarshal([]byte(dispatch(t, e.d, "note_search", fmt.Sprintf(`{"query":%q,"limit":10}`, query))), &hits); err != nil {
		t.Fatal(err)
	}
	refs := make([]string, len(hits))
	for i, h := range hits {
		refs[i] = h.Ref
	}
	return refs
}

func (e *searchEnv) embeddingRows(t *testing.T) int {
	t.Helper()
	var n int
	if err := e.st.DB().QueryRow(`SELECT COUNT(*) FROM embeddings`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

var harbor = map[string]int{"harbor": 0, "ships": 0, "boats": 0, "tax": 1}

func TestNoteSearch_FindsByMeaningAfterWritesAreEmbedded(t *testing.T) {
	srv, requests := embeddingsServer(t, func(s string) []float32 { return wordVector(harbor, s) })
	env := newSearchEnv(t, srv, nil)
	dispatch(t, env.d, "note_write", `{"topic":"dock","text":"boats unload at the quay"}`)
	dispatch(t, env.d, "note_write", `{"topic":"money","text":"tax season is stressful"}`)
	dispatch(t, env.d, "post", `{"text":"ships are late again"}`)
	if env.embeddingRows(t) != 3 {
		t.Fatalf("%d vectors after two notes and a dry-run post; want 3", env.embeddingRows(t))
	}
	got := env.search(t, "harbor")
	if len(got) < 2 || got[0] == "note:2" || got[1] == "note:2" {
		t.Fatalf("search = %v; want the two harbor texts ahead of the tax note without sharing a word", got)
	}
	if len(*requests) != 4 {
		t.Fatalf("%d embedding requests; want 3 writes and 1 query", len(*requests))
	}
	usage, _ := env.st.LLMUsageForDayPhases(context.Background(), "agent", "2026-11-11", "agent")
	if usage.TokensIn != 40 || usage.CostUSD <= 0 {
		t.Fatalf("ledger %+v; want 40 embedding tokens billed to the wake", usage)
	}
	wake, _ := env.st.DB().Query(`SELECT tokens_in FROM wakes WHERE id = 1`)
	defer wake.Close()
	var wakeTokens int64
	if wake.Next() {
		wake.Scan(&wakeTokens)
	}
	if wakeTokens != 40 {
		t.Fatalf("wake tokens = %d, want 40", wakeTokens)
	}
}

func TestNoteSearch_FallsBackToBM25WhenTheQueryCannotBeEmbedded(t *testing.T) {
	down := false
	srv, _ := embeddingsServer(t, func(s string) []float32 {
		if down {
			return nil
		}
		return wordVector(harbor, s)
	})
	env := newSearchEnv(t, srv, nil)
	dispatch(t, env.d, "note_write", `{"topic":"dock","text":"boats unload at the quay"}`)
	dispatch(t, env.d, "note_write", `{"topic":"words","text":"harbor words only"}`)
	down = true
	if got := env.search(t, "harbor"); strings.Join(got, ",") != "note:2" {
		t.Fatalf("fallback search = %v; want the BM25 hit only", got)
	}
	if !strings.Contains(env.logs.String(), `"event":"embedding.failed"`) || !strings.Contains(env.logs.String(), `"reason":"embedding_http_error"`) {
		t.Fatalf("fallback reason not logged:\n%s", env.logs.String())
	}

	if out := dispatch(t, env.d, "note_write", `{"topic":"late","text":"written while embeddings are down"}`); !strings.Contains(out, `"id"`) {
		t.Fatalf("note_write failed with embeddings down: %s", out)
	}
	if env.embeddingRows(t) != 2 {
		t.Fatalf("%d vectors; the late note must stay pending", env.embeddingRows(t))
	}
	down = false
	env.search(t, "harbor")
	dispatch(t, env.d, "note_write", `{"topic":"again","text":"harbor again"}`)
	if env.embeddingRows(t) != 4 {
		t.Fatalf("%d vectors; the next write must catch up the pending note", env.embeddingRows(t))
	}
}

func TestNoteSearch_StopsEmbeddingAtTheDailyCap(t *testing.T) {
	srv, requests := embeddingsServer(t, func(s string) []float32 { return wordVector(harbor, s) })
	env := newSearchEnv(t, srv, nil)
	if err := env.st.RecordUnattributedLLMUsage(context.Background(), "agent", "2026-11-11", store.LLMUsage{CostUSD: 1}); err != nil {
		t.Fatal(err)
	}
	dispatch(t, env.d, "note_write", `{"topic":"words","text":"harbor words only"}`)
	if got := env.search(t, "harbor"); len(got) != 1 || len(*requests) != 0 {
		t.Fatalf("search = %v with %d requests; want BM25 and no spend over the cap", got, len(*requests))
	}
	if !strings.Contains(env.logs.String(), `"reason":"budget_limit"`) {
		t.Fatalf("cap not logged:\n%s", env.logs.String())
	}
}

func TestNoteSearch_NeverReturnsAnotherAgentsRows(t *testing.T) {
	srv, _ := embeddingsServer(t, func(s string) []float32 { return wordVector(harbor, s) })
	env := newSearchEnv(t, srv, nil)
	ctx := context.Background()
	if _, err := env.st.DB().Exec(`INSERT INTO agents(id,status) VALUES('other','running')`); err != nil {
		t.Fatal(err)
	}
	id, err := env.st.WriteTopic(ctx, "other", "t", "", "harbor ships secret", env.opts.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := env.st.SaveEmbeddings(ctx, []store.EmbeddingRow{{Source: store.EmbeddingNote, ID: id, Text: "harbor ships secret", TextHash: store.TextHash("harbor ships secret"), Vector: wordVector(harbor, "harbor")}}); err != nil {
		t.Fatal(err)
	}
	dispatch(t, env.d, "note_write", `{"topic":"mine","text":"tax season"}`)
	if got := env.search(t, "harbor"); strings.Contains(strings.Join(got, ","), fmt.Sprintf("note:%d", id)) {
		t.Fatalf("search returned the other agent's note: %v", got)
	}
	var pending int
	env.st.DB().QueryRow(`SELECT COUNT(*) FROM embeddings`).Scan(&pending)
	if rows, _ := env.st.PendingEmbeddings(ctx, "other", 10); len(rows) != 0 || pending != 2 {
		t.Fatalf("another agent's notes were synced by this wake: pending %v, vectors %d", rows, pending)
	}
}

func TestEmbeddingKeyStaysOutOfToolsLogsAndContext(t *testing.T) {
	srv, _ := embeddingsServer(t, func(s string) []float32 { return wordVector(harbor, s) })
	env := newSearchEnv(t, srv, func(tmpl *config.Template) { tmpl.EmbeddingAPIKey = config.NewSecret(embeddingKeyCanary) })
	for _, c := range []struct{ name, args string }{
		{"note_write", `{"topic":"k","text":"my key ` + embeddingKeyCanary + `"}`},
		{"note_search", `{"query":"` + embeddingKeyCanary + `"}`},
		{"post", `{"text":"` + embeddingKeyCanary + `"}`},
	} {
		if _, err := env.d.Dispatch(context.Background(), call(c.name, c.args)); err == nil {
			t.Fatalf("%s accepted the embedding key", c.name)
		}
	}
	dispatch(t, env.d, "note_write", `{"topic":"ok","text":"harbor"}`)
	env.search(t, "harbor")
	if strings.Contains(env.logs.String(), embeddingKeyCanary) {
		t.Fatalf("key in logs:\n%s", env.logs.String())
	}
	var leaked int
	env.st.DB().QueryRow(`SELECT COUNT(*) FROM tool_calls WHERE args_json LIKE '%canary%' OR result_json LIKE '%canary%'`).Scan(&leaked)
	if leaked != 0 {
		t.Fatal("key persisted in the tool audit")
	}
}

// recallSet is the Vietnamese memory recall set: notes and queries with the
// notes each query should surface.
type recallSet struct {
	Notes   []struct{ ID, Text string } `json:"notes"`
	Queries []struct {
		Q      string   `json:"q"`
		Expect []string `json:"expect"`
	} `json:"queries"`
}

func loadRecallSet(t *testing.T) recallSet {
	t.Helper()
	raw, err := os.ReadFile("testdata/memory_recall.json")
	if err != nil {
		t.Fatal(err)
	}
	var set recallSet
	if err := json.Unmarshal(raw, &set); err != nil {
		t.Fatal(err)
	}
	return set
}

// recordedVectors maps the hash of each recall note and query to its
// text-embedding-3-large vector (base64 of little-endian float32).
type recordedVectors struct {
	Model   string            `json:"model"`
	Vectors map[string]string `json:"vectors"`
}

const recordedVectorsPath = "testdata/memory_recall_embeddings.json"

func loadRecordedVectors(t *testing.T) map[string][]float32 {
	t.Helper()
	raw, err := os.ReadFile(recordedVectorsPath)
	if err != nil {
		t.Fatal(err)
	}
	var rec recordedVectors
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatal(err)
	}
	out := make(map[string][]float32, len(rec.Vectors))
	for hash, enc := range rec.Vectors {
		b, err := base64.StdEncoding.DecodeString(enc)
		if err != nil || len(b) != 4*llm.EmbeddingDimensions {
			t.Fatalf("vector %s: %d bytes, %v", hash, len(b), err)
		}
		vec := make([]float32, llm.EmbeddingDimensions)
		for i := range vec {
			vec[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
		}
		out[hash] = vec
	}
	return out
}

// recallRankings writes every note with note_write and ranks every query
// with note_search, the path the agent takes; it returns note ids per query.
func recallRankings(t *testing.T, set recallSet, srv *httptest.Server) [][]string {
	t.Helper()
	env := newSearchEnv(t, srv, nil)
	byRef := map[string]string{}
	for _, n := range set.Notes {
		args, _ := json.Marshal(map[string]string{"topic": n.ID, "text": n.Text})
		var out struct{ ID int64 }
		if err := json.Unmarshal([]byte(dispatch(t, env.d, "note_write", string(args))), &out); err != nil {
			t.Fatal(err)
		}
		byRef[fmt.Sprintf("note:%d", out.ID)] = n.ID
	}
	ranked := make([][]string, len(set.Queries))
	for i, q := range set.Queries {
		var hits []struct{ Ref string }
		args, _ := json.Marshal(map[string]any{"query": q.Q, "limit": 5})
		if err := json.Unmarshal([]byte(dispatch(t, env.d, "note_search", string(args))), &hits); err != nil {
			t.Fatal(err)
		}
		for _, h := range hits {
			ranked[i] = append(ranked[i], byRef[h.Ref])
		}
	}
	return ranked
}

// recallAt5 is the share of expected notes inside the top five, averaged
// over queries.
func recallAt5(set recallSet, ranked [][]string) float64 {
	var total float64
	for i, ids := range ranked {
		want := map[string]bool{}
		for _, id := range set.Queries[i].Expect {
			want[id] = true
		}
		found := 0
		for rank, id := range ids {
			if want[id] && rank < 5 {
				found++
			}
		}
		total += float64(found) / float64(len(want))
	}
	return total / float64(len(ranked))
}

func TestRecallSet_IsWellFormed(t *testing.T) {
	set := loadRecallSet(t)
	if len(set.Queries) < 30 {
		t.Fatalf("%d queries, want at least 30", len(set.Queries))
	}
	ids := map[string]bool{}
	vectors := loadRecordedVectors(t)
	for _, n := range set.Notes {
		if ids[n.ID] || strings.TrimSpace(n.Text) == "" {
			t.Fatalf("note %q duplicated or empty", n.ID)
		}
		ids[n.ID] = true
		if vectors[store.TextHash(n.Text)] == nil {
			t.Fatalf("note %q has no recorded vector", n.ID)
		}
	}
	for _, q := range set.Queries {
		if len(q.Expect) == 0 || vectors[store.TextHash(q.Q)] == nil {
			t.Fatalf("query %q expects nothing or has no recorded vector", q.Q)
		}
		for _, id := range q.Expect {
			if !ids[id] {
				t.Fatalf("query %q expects unknown note %q", q.Q, id)
			}
		}
	}
}

func TestRecallAt5(t *testing.T) {
	set := recallSet{Queries: []struct {
		Q      string   `json:"q"`
		Expect []string `json:"expect"`
	}{{Expect: []string{"b"}}, {Expect: []string{"b", "q"}}, {Expect: []string{"b"}}}}
	got := recallAt5(set, [][]string{{"a", "b"}, {"x", "y", "z", "w", "v", "b"}, {"z"}})
	if want := 1.0 / 3; math.Abs(got-want) > 1e-9 {
		t.Fatalf("recall@5 = %v, want %v", got, want)
	}
}

func TestMemoryRecall_HybridBeatsBM25OnRecordedEmbeddings(t *testing.T) {
	set := loadRecallSet(t)
	vectors := loadRecordedVectors(t)
	working, _ := embeddingsServer(t, func(s string) []float32 { return vectors[store.TextHash(s)] })
	down, _ := embeddingsServer(t, func(string) []float32 { return nil })

	hybrid := recallAt5(set, recallRankings(t, set, working))
	bm25 := recallAt5(set, recallRankings(t, set, down))
	t.Logf("recall@5 hybrid=%.3f bm25=%.3f", hybrid, bm25)
	if hybrid <= bm25 || hybrid < 0.9 {
		t.Fatalf("hybrid recall@5 %.3f vs BM25 %.3f; want hybrid clearly ahead", hybrid, bm25)
	}
}

// recordingTransport keeps every vector the live API returns, keyed by text hash.
type recordingTransport struct {
	vectors map[string]string
}

func (r *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	body, _ := io.ReadAll(req.Body)
	req.Body = io.NopCloser(bytes.NewReader(body))
	res, err := http.DefaultTransport.RoundTrip(req)
	if err != nil || res.StatusCode != http.StatusOK {
		return res, err
	}
	raw, _ := io.ReadAll(res.Body)
	res.Body = io.NopCloser(bytes.NewReader(raw))
	var in struct{ Input []string }
	var out struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		}
	}
	if json.Unmarshal(body, &in) == nil && json.Unmarshal(raw, &out) == nil {
		for _, d := range out.Data {
			buf := make([]byte, 4*len(d.Embedding))
			for i, f := range d.Embedding {
				binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(f))
			}
			r.vectors[store.TextHash(in.Input[d.Index])] = base64.StdEncoding.EncodeToString(buf)
		}
	}
	return res, nil
}

// Live and paid: THREADZ_LIVE_EMBED=1 and OPENAI_API_KEY. With
// THREADZ_RECORD_EMBEDDINGS=1 it also rewrites the recorded vectors the
// offline test reads.
func TestMemoryRecall_LiveEmbeddings(t *testing.T) {
	if os.Getenv("THREADZ_LIVE_EMBED") != "1" {
		t.Skip("set THREADZ_LIVE_EMBED=1 and OPENAI_API_KEY to spend on embeddings")
	}
	key := os.Getenv("OPENAI_API_KEY")
	if key == "" {
		t.Fatal("OPENAI_API_KEY is empty")
	}
	set := loadRecallSet(t)
	recorder := &recordingTransport{vectors: map[string]string{}}
	st, opts, _ := setup(t)
	opts.Template.EmbeddingAPIKey = config.NewSecret(key)
	emb, err := llm.NewEmbedder(llm.EmbeddingBaseURL, config.NewSecret(key), config.LimitsConfig{MaxUSDPerDay: 1}, st, llm.Options{Now: opts.Now, HTTPClient: &http.Client{Transport: recorder}, RequestTimeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, n := range set.Notes {
		if _, err := st.WriteTopic(ctx, "agent", n.ID, "", n.Text, opts.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := emb.Sync(ctx, "agent", 1); err != nil {
		t.Fatal(err)
	}
	for _, q := range set.Queries {
		if _, err := emb.Embed(ctx, "agent", 1, []string{q.Q}); err != nil {
			t.Fatal(err)
		}
	}
	usage, _ := st.LLMUsageForDay(ctx, "agent", "2026-11-11")
	t.Logf("embedded %d notes and %d queries: tokens=%d USD=%.6f", len(set.Notes), len(set.Queries), usage.TokensIn, usage.CostUSD)

	if os.Getenv("THREADZ_RECORD_EMBEDDINGS") == "1" {
		raw, _ := json.Marshal(recordedVectors{Model: llm.EmbeddingModel, Vectors: recorder.vectors})
		if err := os.WriteFile(filepath.Join(recordedVectorsPath), append(raw, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	live, _ := embeddingsServer(t, func(s string) []float32 {
		enc, ok := recorder.vectors[store.TextHash(s)]
		if !ok {
			return nil
		}
		b, _ := base64.StdEncoding.DecodeString(enc)
		vec := make([]float32, llm.EmbeddingDimensions)
		for i := range vec {
			vec[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
		}
		return vec
	})
	down, _ := embeddingsServer(t, func(string) []float32 { return nil })
	t.Logf("recall@5 hybrid=%.3f bm25=%.3f", recallAt5(set, recallRankings(t, set, live)), recallAt5(set, recallRankings(t, set, down)))
}
