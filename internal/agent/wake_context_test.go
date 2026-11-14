package agent_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/agent"
	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/store"
)

type exampleRow struct {
	ID    int64  `json:"id"`
	Text  string `json:"text"`
	Likes int64  `json:"likes"`
	Band  string `json:"band"`
}

type kindExamples struct {
	Top    []exampleRow `json:"top"`
	Bottom []exampleRow `json:"bottom"`
}

type wakeData struct {
	Said    []string `json:"said"`
	Lessons []struct {
		ID   int64  `json:"id"`
		Text string `json:"text"`
	} `json:"lessons"`
	Examples struct {
		Post  kindExamples `json:"post"`
		Reply kindExamples `json:"reply"`
	} `json:"examples"`
	Recent []struct {
		ID   int64  `json:"id"`
		Kind string `json:"kind"`
		AgeH int    `json:"age_h"`
		Band string `json:"band"`
	} `json:"recent"`
	Counters struct {
		Posts   int `json:"posts_today"`
		Replies int `json:"replies_today"`
	} `json:"counters"`
}

func contextRuntime(t *testing.T, tmpl config.Template, now time.Time) (*agent.Runtime, *store.Store) {
	t.Helper()
	st, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	r, err := agent.New(agent.Options{Store: st, Template: tmpl, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	return r, st
}

func wakeMessages(t *testing.T, r *agent.Runtime) (string, string, wakeData) {
	t.Helper()
	messages, err := r.Context(context.Background(), "agent")
	if err != nil {
		t.Fatal(err)
	}
	var data wakeData
	firstJSON(t, messages[1].Content, &data)
	return messages[0].Content, messages[1].Content, data
}

func TestSystemPromptGolden(t *testing.T) {
	tmpls, err := config.Load("../../templates", config.Options{Getenv: func(string) string { return "dummy" }})
	if err != nil {
		t.Fatal(err)
	}
	viral := tmpls[len(tmpls)-1] // sorted by file name; viral sorts last
	if viral.ID != "viral" {
		t.Fatalf("last template = %q, want viral", viral.ID)
	}
	r, _ := contextRuntime(t, viral, time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC))
	messages, err := r.Context(context.Background(), "agent")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join("testdata", "viral.system.golden")
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err = os.WriteFile(path, []byte(messages[0].Content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if messages[0].Role != "system" || messages[0].Content != string(want) {
		t.Fatalf("system prompt differs from %s; rerun with UPDATE_GOLDEN=1 after reviewing\n%s", path, messages[0].Content)
	}
}

func TestWakeContextOrdersStableBeforeVolatile(t *testing.T) {
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	tmpl := agent.OfflineTemplate()
	r, st := contextRuntime(t, tmpl, now)
	for i := int64(0); i < 9; i++ {
		seedEpisode(t, st, tmpl.ID, "post", fmt.Sprintf("post %d", i), now.Add(-time.Duration(30+i)*time.Hour), 6, 100+i, i, 0)
	}
	seedLesson(t, st, tmpl.ID, "post", "do", "open with the number", now.Add(-48*time.Hour), now.Add(-time.Hour))
	_, user, _ := wakeMessages(t, r)
	last := -1
	for _, key := range []string{`"lessons"`, `"examples"`, `"recent"`, `"owner_answers"`, `"facts"`, `"remaining_usd"`, `"counters"`, `"said"`} {
		i := strings.Index(user, key)
		if i <= last {
			t.Fatalf("%s is out of order in\n%.600s", key, user)
		}
		last = i
	}
}

func TestWakeContextHoldsOnlyOwnEpisodesAndLessons(t *testing.T) {
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	tmpl := agent.OfflineTemplate()
	r, st := contextRuntime(t, tmpl, now)
	var ids []int64
	for i := int64(0); i < 8; i++ {
		ids = append(ids, seedEpisode(t, st, tmpl.ID, "post", fmt.Sprintf("own post %d", i), now.Add(-time.Duration(30+i)*time.Hour), 6, 100+i, i, 0))
	}
	ids = append(ids, seedEpisode(t, st, tmpl.ID, "post", "leaked offline-dummy-key here", now.Add(-time.Hour), 6, 500, 20, 0))
	for i := int64(0); i < 9; i++ {
		seedEpisode(t, st, "other", "post", "OTHER AGENT TEXT", now.Add(-time.Duration(30+i)*time.Hour), 6, 100, 50+i, 0)
	}
	if _, err := st.SeedAction(context.Background(), store.SeededAction{AgentID: tmpl.ID, Kind: "post", Text: "DRY RUN TEXT", DryRun: true, PostedAt: now.Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	seedLesson(t, st, tmpl.ID, "post", "do", "own lesson", now.Add(-48*time.Hour), now.Add(-time.Hour))
	seedLesson(t, st, "other", "post", "do", "OTHER LESSON", now.Add(-48*time.Hour), now.Add(-time.Hour))
	retired := seedLesson(t, st, tmpl.ID, "post", "do", "RETIRED LESSON", now.Add(-48*time.Hour), now.Add(-time.Hour))
	if err := st.RetireLesson(context.Background(), tmpl.ID, retired, store.RetiredByDream, "x", now); err != nil {
		t.Fatal(err)
	}
	system, user, data := wakeMessages(t, r)
	for _, secret := range []string{"OTHER AGENT TEXT", "OTHER LESSON", "RETIRED LESSON", "offline-dummy-key"} {
		if strings.Contains(system+user, secret) {
			t.Fatalf("%q reached the context", secret)
		}
	}
	// A dry run is never an episode, but said shows it in its own mode.
	if !tmpl.DryRun {
		t.Fatal("offline template is expected to be a dry run")
	}
	if len(data.Said) == 0 || data.Said[0] != "DRY RUN TEXT" {
		t.Fatalf("said=%q, want the newest dry-run text first", data.Said)
	}
	if n := strings.Count(user, "DRY RUN TEXT"); n != 1 {
		t.Fatalf("the dry run appears %d times; only said may show it", n)
	}
	if len(data.Lessons) != 1 || data.Lessons[0].Text != "own lesson" {
		t.Fatalf("lessons=%+v", data.Lessons)
	}
	post := data.Examples.Post
	if len(post.Top) != 3 || post.Top[0].ID != ids[8] || post.Top[0].Likes != 20 || len(post.Bottom) != 3 || post.Bottom[0].Likes != 0 {
		t.Fatalf("examples=%+v", post)
	}
	if len(data.Examples.Reply.Top)+len(data.Examples.Reply.Bottom) != 0 {
		t.Fatalf("reply examples without replies: %+v", data.Examples.Reply)
	}
	if len(data.Recent) != 9 || data.Recent[0].ID != ids[8] || data.Recent[0].AgeH != 1 {
		t.Fatalf("recent=%+v", data.Recent)
	}
}

func TestExamplesAreTheLast48Hours(t *testing.T) {
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	tmpl := agent.OfflineTemplate()
	r, st := contextRuntime(t, tmpl, now)
	for i := int64(0); i < 9; i++ {
		seedEpisode(t, st, tmpl.ID, "post", "old", now.Add(-time.Duration(60+i)*time.Hour), 6, 100, i, 0)
	}
	_, _, data := wakeMessages(t, r)
	if len(data.Examples.Post.Top)+len(data.Examples.Post.Bottom) != 0 || len(data.Recent) != 9 {
		t.Fatalf("examples=%+v recent=%d", data.Examples.Post, len(data.Recent))
	}
}

// With 30 episodes and 10 lessons the data message, scoreboard included and
// inbox excluded, stays inside its budget.
func TestWakeContextStaysUnderSevenKilobytes(t *testing.T) {
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	tmpl := agent.OfflineTemplate()
	tmpl.KPI = config.KPIConfig{EngagementsPerDay: 10, FollowersPerWeek: 5, MaxHoursWithoutPublish: 8, OnTargetMinutes: 180}
	r, st := contextRuntime(t, tmpl, now)
	post := strings.Repeat("Hôm nay build xong trong bốn phút, nhanh hơn cả ly cà phê nguội. ", 2)
	reply := "ủa vậy hả, thế là tôi bị lừa cả năm nay rồi sao, đau lòng thật sự luôn á"
	for i := int64(0); i < 30; i++ {
		kind, text := "post", post
		if i%2 == 1 {
			kind, text = "reply", reply
		}
		seedEpisode(t, st, tmpl.ID, kind, text, now.Add(-time.Duration(8+i)*time.Hour), 6, 100+37*i%91, i*7%13, i%4)
	}
	lesson := "Mở bài bằng một con số cụ thể rồi mới kể chuyện thường được trả lời nhiều hơn."
	for i := 0; i < 10; i++ {
		seedLesson(t, st, tmpl.ID, "any", "do", fmt.Sprintf("%s %d", lesson, i), now.Add(-48*time.Hour), now.Add(-time.Hour))
	}
	// The template is a dry run, so said shows these, the size a live wake sees.
	for i := 0; i < 10; i++ {
		if _, err := st.SeedAction(context.Background(), store.SeededAction{AgentID: tmpl.ID, Kind: "reply", Text: reply, DryRun: true, PostedAt: now.Add(-time.Duration(i) * time.Minute)}); err != nil {
			t.Fatal(err)
		}
	}
	_, user, data := wakeMessages(t, r)
	t.Logf("data message: %d bytes", len(user))
	if len(user) > 7<<10 {
		t.Fatalf("data message is %d bytes, want at most %d", len(user), 7<<10)
	}
	if len(data.Said) != 10 || len(data.Lessons) != 10 || len(data.Recent) != 10 || len(data.Examples.Post.Top) != 3 || len(data.Examples.Reply.Bottom) != 3 {
		t.Fatalf("a realistic state must fill every section: %d lessons, %d recent, %+v", len(data.Lessons), len(data.Recent), data.Examples)
	}
}
