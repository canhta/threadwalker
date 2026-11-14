package agent_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/agent"
	"github.com/canhta/threadz/internal/llm"
	"github.com/canhta/threadz/internal/store"
)

type dreamRig struct {
	r       *agent.Runtime
	st      *store.Store
	fixture *agent.OfflineFixture
	tmpl    string
	now     time.Time
	sent    [][]llm.Message
}

func newDreamRig(t *testing.T) *dreamRig {
	t.Helper()
	g := &dreamRig{now: time.Date(2026, 1, 3, 3, 0, 0, 0, time.UTC)}
	tmpl := agent.OfflineTemplate()
	tmpl.Limits.MaxUSDPerDream, tmpl.Limits.MaxStepsPerDream = 1, 8
	st, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	g.st, g.tmpl = st, tmpl.ID
	g.fixture = agent.NewOfflineFixture(func() time.Time { return g.now })
	g.fixture.Calls = []llm.ToolCall{}
	g.fixture.Observe = func(m []llm.Message) { g.sent = append(g.sent, append([]llm.Message(nil), m...)) }
	g.r, err = agent.New(agent.Options{Store: st, Template: tmpl, Now: func() time.Time { return g.now }, HTTPClient: g.fixture.Client(), FetchTrends: g.fixture.Trends})
	if err != nil {
		t.Fatal(err)
	}
	if err = g.r.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	return g
}

// episodes seeds n posts measured at 6h that the dream has not seen; likes
// rise with the index, so the last ones are top and the first ones bottom.
func (g *dreamRig) episodes(t *testing.T, n int) []int64 {
	var ids []int64
	for i := 0; i < n; i++ {
		ids = append(ids, seedEpisode(t, g.st, g.tmpl, "post", fmt.Sprintf("post number %d", i), g.now.Add(-time.Duration(40-i)*time.Hour), 6, 100, int64(i), 0))
	}
	return ids
}

func (g *dreamRig) endReason(t *testing.T, key string) string {
	t.Helper()
	var reason string
	var ended *string
	if err := g.st.DB().QueryRow(`SELECT end_reason,ended_at FROM wakes WHERE agent_id=? AND kind='dream' AND phase_key=?`, g.tmpl, key).Scan(&reason, &ended); err != nil || ended == nil {
		t.Fatalf("dream wake %s: reason=%q ended=%v err=%v", key, reason, ended, err)
	}
	return reason
}

func TestDreamSkipsWithFewerThanFiveEpisodesAndCallsNoModel(t *testing.T) {
	ctx := context.Background()
	g := newDreamRig(t)
	g.episodes(t, 4)
	// A 1h sample is not a 6h rank; an episode of another agent is not ours.
	seedEpisode(t, g.st, g.tmpl, "post", "early", g.now.Add(-5*time.Hour), 1, 10, 1, 0)
	seedEpisode(t, g.st, "other", "post", "foreign", g.now.Add(-20*time.Hour), 6, 10, 1, 0)
	wake, err := g.r.RunPhase(ctx, store.WakeDream, "2026-01-03")
	if err != nil || wake.EndReason != "too_few_episodes" {
		t.Fatalf("wake=%+v err=%v", wake, err)
	}
	if len(g.sent) != 0 {
		t.Fatalf("the model was called %d times", len(g.sent))
	}
	if got := g.endReason(t, "2026-01-03"); got != "too_few_episodes" {
		t.Fatalf("end_reason=%q", got)
	}
	if again, err := g.r.RunPhase(ctx, store.WakeDream, "2026-01-03"); err != nil || again.EndReason != "already_ran" {
		t.Fatalf("a skipped dream used up its day: %+v %v", again, err)
	}
}

func TestDreamRunsOnTheNewEpisodesAndOnlyCountsEpisodesAfterTheWatermark(t *testing.T) {
	ctx := context.Background()
	g := newDreamRig(t)
	ids := g.episodes(t, 9)
	// A lesson nobody confirmed or applied for 15 days expires before the turn;
	// a young one shows with its age.
	seedLesson(t, g.st, g.tmpl, "post", "do", "stale lesson", g.now.Add(-20*24*time.Hour), g.now.Add(-15*24*time.Hour))
	young := seedLesson(t, g.st, g.tmpl, "reply", "avoid", "young lesson", g.now.Add(-3*24*time.Hour), g.now.Add(-24*time.Hour))
	g.fixture.Calls = []llm.ToolCall{agent.SyntheticCall("lesson_add", fmt.Sprintf(`{"text":"open with the plain claim","kind":"post","polarity":"do","action_ids":[%d,%d]}`, ids[8], ids[7]))}

	wake, err := g.r.RunPhase(ctx, store.WakeDream, "2026-01-03")
	if err != nil || wake.EndReason != "completed" {
		t.Fatalf("wake=%+v err=%v", wake, err)
	}
	first := g.sent[0]
	if first[0].Content == "" || strings.Contains(first[0].Content, "Synthetic fixture") {
		t.Fatalf("system prompt: %q", first[0].Content)
	}
	var data struct {
		Lessons []struct {
			ID      int64  `json:"id"`
			Text    string `json:"text"`
			AgeDays int    `json:"age_days"`
			Applied int    `json:"applied_n"`
		} `json:"lessons"`
		New  []struct{ ID int64 } `json:"new_episodes"`
		Week struct {
			Post struct{ Top, Bottom []struct{ ID int64 } } `json:"post"`
		} `json:"last_7_days"`
	}
	if err = json.Unmarshal([]byte(first[1].Content), &data); err != nil {
		t.Fatal(err)
	}
	if len(data.Lessons) != 1 || data.Lessons[0].ID != young || data.Lessons[0].AgeDays != 3 {
		t.Fatalf("lessons=%+v", data.Lessons)
	}
	if strings.Contains(first[1].Content, "applied_median_rank") || strings.Contains(first[1].Content, "others_median_rank") {
		t.Fatalf("a median without actions behind it: %s", first[1].Content)
	}
	if len(data.New) != 9 || len(data.Week.Post.Top) != 3 || data.Week.Post.Top[0].ID != ids[8] || len(data.Week.Post.Bottom) != 3 {
		t.Fatalf("new=%d week=%+v", len(data.New), data.Week)
	}
	active, err := g.st.Lessons(ctx, g.tmpl, false)
	if err != nil || len(active) != 2 {
		t.Fatalf("active lessons=%+v err=%v", active, err)
	}
	all, _ := g.st.Lessons(ctx, g.tmpl, true)
	if len(all) != 3 || all[2].RetiredBy != store.RetiredByExpiry {
		t.Fatalf("the stale lesson was not expired: %+v", all)
	}

	// The next dream counts only episodes measured after this one ended.
	g.now = g.now.Add(24 * time.Hour)
	g.sent = nil
	if wake, err = g.r.RunPhase(ctx, store.WakeDream, "2026-01-04"); err != nil || wake.EndReason != "too_few_episodes" || len(g.sent) != 0 {
		t.Fatalf("second dream: %+v err=%v calls=%d", wake, err, len(g.sent))
	}
	for i := 0; i < 5; i++ {
		seedEpisode(t, g.st, g.tmpl, "post", "fresh", g.now.Add(-8*time.Hour), 6, 100, int64(i), 0)
	}
	g.now = g.now.Add(24 * time.Hour)
	if wake, err = g.r.RunPhase(ctx, store.WakeDream, "2026-01-05"); err != nil || wake.EndReason != "completed" {
		t.Fatalf("third dream: %+v err=%v", wake, err)
	}
}
