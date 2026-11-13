package store_test

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/store"
)

var embedAt = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)

func saveVector(t *testing.T, st *store.Store, source string, id int64, text string, vec []float32) {
	t.Helper()
	if err := st.SaveEmbeddings(context.Background(), []store.EmbeddingRow{{Source: source, ID: id, Text: text, TextHash: store.TextHash(text), Vector: vec}}); err != nil {
		t.Fatal(err)
	}
}

func refList(hits []store.MemoryHit) string {
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.Ref
	}
	return strings.Join(out, ",")
}

func TestPendingEmbeddings_ListsOnlyOwnRowsWithoutVectors(t *testing.T) {
	ctx := context.Background()
	st := openNotesTestStore(t)
	insertNotesTestAgent(t, st, "a")
	insertNotesTestAgent(t, st, "b")
	n1, _ := writeNote(st, ctx, "a", "first note", embedAt)
	n2, _ := writeNote(st, ctx, "a", "second note", embedAt)
	act := insertAction(t, st, "a", "post", "a post", embedAt)
	insertAction(t, st, "a", "post", "   ", embedAt)
	if _, err := writeNote(st, ctx, "b", "other agent", embedAt); err != nil {
		t.Fatal(err)
	}
	saveVector(t, st, store.EmbeddingNote, n1, "first note", []float32{1})

	got, err := st.PendingEmbeddings(ctx, "a", 10)
	if err != nil || len(got) != 2 || got[0].Source != store.EmbeddingAction || got[0].ID != act || got[1].ID != n2 {
		t.Fatalf("pending = %+v, %v; want action %d then note %d", got, err, act, n2)
	}
	if got, _ := st.PendingEmbeddings(ctx, "a", 1); len(got) != 1 {
		t.Fatalf("limit ignored: %+v", got)
	}
}

func TestEmbeddings_FollowTheirSourceText(t *testing.T) {
	ctx := context.Background()
	st := openNotesTestStore(t)
	insertNotesTestAgent(t, st, "a")
	id, _ := st.WriteTopic(ctx, "a", "t", "", "first", embedAt)
	act := insertAction(t, st, "a", "post", "post text", embedAt)
	saveVector(t, st, store.EmbeddingNote, id, "first", []float32{1})
	saveVector(t, st, store.EmbeddingAction, act, "post text", []float32{1})

	st.WriteTopic(ctx, "a", "t", "new description", "first", embedAt.Add(time.Hour))
	if n := countRows(t, st, `SELECT COUNT(*) FROM embeddings`); n != 2 {
		t.Fatalf("unchanged text dropped its vector: %d rows", n)
	}
	st.WriteTopic(ctx, "a", "t", "", "second", embedAt.Add(time.Hour))
	if n := countRows(t, st, `SELECT COUNT(*) FROM embeddings WHERE source = 'note'`); n != 0 {
		t.Fatal("changed text kept the old vector")
	}
	saveVector(t, st, store.EmbeddingNote, id, "second", []float32{2})
	if _, err := st.DB().ExecContext(ctx, `UPDATE actions SET text = 'edited' WHERE id = ?`, act); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().ExecContext(ctx, `DELETE FROM notes WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, st, `SELECT COUNT(*) FROM embeddings`); n != 0 {
		t.Fatalf("%d vectors outlived their rows", n)
	}
	saveVector(t, st, store.EmbeddingAction, act, "edited", []float32{3})
	if _, err := st.DB().ExecContext(ctx, `DELETE FROM actions WHERE id = ?`, act); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, st, `SELECT COUNT(*) FROM embeddings`); n != 0 {
		t.Fatalf("%d vectors outlived their action", n)
	}
}

func TestSaveEmbeddings_SkipsRowRewrittenMeanwhile(t *testing.T) {
	ctx := context.Background()
	st := openNotesTestStore(t)
	insertNotesTestAgent(t, st, "a")
	id, _ := st.WriteTopic(ctx, "a", "t", "", "current", embedAt)
	saveVector(t, st, store.EmbeddingNote, id, "stale", []float32{1})
	if n := countRows(t, st, `SELECT COUNT(*) FROM embeddings`); n != 0 {
		t.Fatal("stored a vector for text the row no longer holds")
	}
}

func TestVectorsByHash(t *testing.T) {
	ctx := context.Background()
	st := openNotesTestStore(t)
	insertNotesTestAgent(t, st, "a")
	id, _ := writeNote(st, ctx, "a", "known", embedAt)
	saveVector(t, st, store.EmbeddingNote, id, "known", []float32{0.5, -2})
	got, err := st.VectorsByHash(ctx, []string{store.TextHash("known"), store.TextHash("unknown")})
	if err != nil || len(got) != 1 || got[store.TextHash("known")][1] != -2 {
		t.Fatalf("VectorsByHash = %v, %v", got, err)
	}
}

func TestRecordUnattributedLLMUsage(t *testing.T) {
	ctx := context.Background()
	st := openNotesTestStore(t)
	insertNotesTestAgent(t, st, "a")
	for i := 0; i < 2; i++ {
		if err := st.RecordUnattributedLLMUsage(ctx, "a", "2026-11-12", store.LLMUsage{TokensIn: 10, CostUSD: 0.5}); err != nil {
			t.Fatal(err)
		}
	}
	total, _ := st.LLMUsageForDay(ctx, "a", "2026-11-12")
	phase, _ := st.LLMUsageForDayPhases(ctx, "a", "2026-11-12", "agent")
	if total.TokensIn != 20 || total.CostUSD != 1 || phase != total {
		t.Fatalf("ledger %+v, agent phase %+v", total, phase)
	}
	if err := st.RecordUnattributedLLMUsage(ctx, "a", "2026-11-12", store.LLMUsage{CostUSD: -1}); err == nil {
		t.Fatal("negative cost accepted")
	}
}

func TestSearchMemory_FusesSemanticAndLexicalRankings(t *testing.T) {
	ctx := context.Background()
	st := openNotesTestStore(t)
	insertNotesTestAgent(t, st, "a")
	insertNotesTestAgent(t, st, "b")
	lexical, _ := writeNote(st, ctx, "a", "harbor cranes at dawn", embedAt)
	semantic, _ := writeNote(st, ctx, "a", "ships unloading "+strings.Repeat("x ", 200), embedAt.Add(time.Hour))
	post := insertAction(t, st, "a", "post", "unrelated post", embedAt.Add(2*time.Hour))
	foreign, _ := writeNote(st, ctx, "b", "harbor ships", embedAt)
	odd, _ := writeNote(st, ctx, "a", "harbor wrong dimension", embedAt)
	saveVector(t, st, store.EmbeddingNote, lexical, "harbor cranes at dawn", []float32{0, 1})
	saveVector(t, st, store.EmbeddingNote, semantic, "ships unloading "+strings.Repeat("x ", 200), []float32{1, 0})
	saveVector(t, st, store.EmbeddingAction, post, "unrelated post", []float32{-1, 0})
	saveVector(t, st, store.EmbeddingNote, foreign, "harbor ships", []float32{1, 0})
	saveVector(t, st, store.EmbeddingNote, odd, "harbor wrong dimension", []float32{1, 0, 0})
	var none time.Time

	bm25, err := st.SearchMemory(ctx, "a", "harbor", nil, none, none, 10)
	if err != nil || refList(bm25) != "note:"+itoa(odd)+",note:"+itoa(lexical) && refList(bm25) != "note:"+itoa(lexical)+",note:"+itoa(odd) {
		t.Fatalf("nil vector = %v, %v; want the BM25 hits only", refList(bm25), err)
	}
	hits, err := st.SearchMemory(ctx, "a", "harbor", []float32{1, 0}, none, none, 10)
	if err != nil {
		t.Fatal(err)
	}
	got := refList(hits)
	if !strings.HasPrefix(got, "note:"+itoa(lexical)) && !strings.HasPrefix(got, "note:"+itoa(semantic)) {
		t.Fatalf("fused order = %s; want a hit from both rankings first", got)
	}
	for _, id := range []int64{semantic, post} {
		if !strings.Contains(got, itoa(id)) {
			t.Fatalf("fused = %s, missing semantic-only hit %d", got, id)
		}
	}
	if strings.Contains(got, "note:"+itoa(foreign)) {
		t.Fatalf("another agent's note returned: %s", got)
	}
	for _, h := range hits {
		if h.Ref == "note:"+itoa(semantic) && (strings.Contains(h.Snippet, "[") || !strings.HasSuffix(h.Snippet, "…") || len([]rune(h.Snippet)) > 161) {
			t.Fatalf("semantic-only snippet = %q; want a bounded text prefix", h.Snippet)
		}
	}

	late, _ := st.SearchMemory(ctx, "a", "harbor", []float32{1, 0}, embedAt.Add(30*time.Minute), none, 10)
	if strings.Contains(refList(late), "note:"+itoa(lexical)) {
		t.Fatalf("since bound ignored for notes: %s", refList(late))
	}
	early, _ := st.SearchMemory(ctx, "a", "harbor", []float32{1, 0}, none, embedAt.Add(90*time.Minute), 10)
	if strings.Contains(refList(early), "action:") {
		t.Fatalf("until bound ignored for actions: %s", refList(early))
	}
	one, _ := st.SearchMemory(ctx, "a", "harbor", []float32{1, 0}, none, none, 1)
	if len(one) != 1 {
		t.Fatalf("limit ignored: %v", one)
	}
}

func TestSearchMemory_FusionTiesGoToTheNewest(t *testing.T) {
	ctx := context.Background()
	st := openNotesTestStore(t)
	insertNotesTestAgent(t, st, "a")
	old, _ := writeNote(st, ctx, "a", "alpha", embedAt)
	recent, _ := writeNote(st, ctx, "a", "beta", embedAt.Add(time.Hour))
	saveVector(t, st, store.EmbeddingNote, old, "alpha", []float32{1, 0})
	saveVector(t, st, store.EmbeddingNote, recent, "beta", []float32{0, 1})
	// "alpha" ranks first lexically and "beta" first semantically: equal scores.
	hits, err := st.SearchMemory(ctx, "a", "alpha beta", []float32{0, 1}, time.Time{}, time.Time{}, 10)
	if err != nil || refList(hits) != "note:"+itoa(recent)+",note:"+itoa(old) {
		t.Fatalf("hits = %v, %v; want the newest first on a tie", refList(hits), err)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
