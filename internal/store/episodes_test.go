package store_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/store"
)

var epoch = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)

// seed inserts a live confirmed action posted at epoch+id hours. completed maps
// a metric window to the hours after posting its sample was taken; every window
// in the map is completed by that one sample, so a late sample can serve several.
func seed(t *testing.T, st *store.Store, agent, kind string, views, likes, replies int64, windows []int, measuredAfter time.Duration) int64 {
	t.Helper()
	db := st.DB()
	var next int64
	if err := db.QueryRow(`SELECT COALESCE(MAX(id),0)+1 FROM actions`).Scan(&next); err != nil {
		t.Fatal(err)
	}
	posted := epoch.Add(time.Duration(next) * time.Hour)
	res, err := db.Exec(`INSERT INTO actions(agent_id,kind,text,dry_run,posted_at,thread_url,media_id,hook,angle,fact,parent_replies,parent_age_minutes) VALUES(?,?,?,0,?,?,?,?,?,?,?,?)`,
		agent, kind, fmt.Sprintf("text %d", next), posted.Format(time.RFC3339Nano), fmt.Sprintf("https://threads.test/@a/post/%d", next), fmt.Sprintf("m%d", next), "number", "angle", "fact", 4, 30)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	measured := posted.Add(measuredAfter).Format(time.RFC3339Nano)
	if _, err = db.Exec(`INSERT INTO metrics(agent_id,action_id,measured_at,views,likes,replies) VALUES(?,?,?,?,?,?)`, agent, id, measured, views, likes, replies); err != nil {
		t.Fatal(err)
	}
	for _, w := range []int{1, 6, 24} {
		var done any
		for _, c := range windows {
			if c == w {
				done = measured
			}
		}
		due := posted.Add(time.Duration(w) * time.Hour).Format(time.RFC3339Nano)
		if _, err = db.Exec(`INSERT INTO metric_jobs(agent_id,action_id,window_hours,due_at,not_before,completed_at) VALUES(?,?,?,?,?,?)`, agent, id, w, due, due, done); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

// seedLikes posts one post per entry, each measured at 6h with that many likes.
func seedLikes(t *testing.T, st *store.Store, agent string, likes ...int64) []int64 {
	t.Helper()
	var ids []int64
	for _, l := range likes {
		ids = append(ids, seed(t, st, agent, "post", l, l, 0, []int{6}, 6*time.Hour))
	}
	return ids
}

func episodeOf(t *testing.T, st *store.Store, agent string, id int64) store.Episode {
	t.Helper()
	eps, err := st.Episodes(context.Background(), agent, store.EpisodeQuery{})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range eps {
		if e.ActionID == id {
			return e
		}
	}
	t.Fatalf("no episode for action %d", id)
	return store.Episode{}
}

func TestEpisodeRankAndBand(t *testing.T) {
	st := openTestStore(t)
	insertTestAgent(t, st, "a")
	ids := seedLikes(t, st, "a", 5, 3, 8, 1, 9, 0, 7, 2, 6, 4)
	for i, want := range []string{"mid", "mid", "top", "bottom", "top", "bottom", "top", "bottom", "mid", "mid"} {
		e := episodeOf(t, st, "a", ids[i])
		if e.Band != want || e.Pool != 10 || e.Window != 6 {
			t.Errorf("action %d: band %s pool %d window %d, want %s 10 6", ids[i], e.Band, e.Pool, e.Window, want)
		}
	}
	if e := episodeOf(t, st, "a", ids[4]); e.Rank != 1 || e.Score != 9 || e.Hook != "number" || e.Angle != "angle" || e.Fact != "fact" || e.ParentReplies == nil || *e.ParentReplies != 4 || *e.ParentAgeMinutes != 30 {
		t.Errorf("best episode: %+v", e)
	}
	if e := episodeOf(t, st, "a", ids[5]); e.Rank != 0 {
		t.Errorf("worst rank %v", e.Rank)
	}
}

func TestScoreWeighsRepliesTwiceAndViewsBreakTies(t *testing.T) {
	st := openTestStore(t)
	insertTestAgent(t, st, "a")
	var ids []int64
	for range 6 {
		ids = append(ids, seed(t, st, "a", "post", 10, 1, 0, []int{6}, 6*time.Hour))
	}
	replied := seed(t, st, "a", "post", 10, 0, 2, []int{6}, 6*time.Hour) // score 4
	liked := seed(t, st, "a", "post", 10, 3, 0, []int{6}, 6*time.Hour)   // score 3
	seen := seed(t, st, "a", "post", 99, 1, 0, []int{6}, 6*time.Hour)    // score 1, most views
	if a, b := episodeOf(t, st, "a", replied), episodeOf(t, st, "a", liked); a.Score != 4 || b.Score != 3 || a.Rank <= b.Rank {
		t.Errorf("replied %+v liked %+v", a, b)
	}
	if a, b := episodeOf(t, st, "a", seen), episodeOf(t, st, "a", ids[0]); a.Rank <= b.Rank {
		t.Errorf("views must break the tie: %v <= %v", a.Rank, b.Rank)
	}
}

func TestEpisodeUnrankedBelowEight(t *testing.T) {
	st := openTestStore(t)
	insertTestAgent(t, st, "a")
	ids := seedLikes(t, st, "a", 1, 2, 3, 4, 5, 6, 7)
	e := episodeOf(t, st, "a", ids[6])
	if e.Band != store.BandUnranked || e.Rank != 0 || e.Score != 7 {
		t.Fatalf("7 comparable: %+v", e)
	}
	seedLikes(t, st, "a", 8)
	if e = episodeOf(t, st, "a", ids[6]); e.Band == store.BandUnranked {
		t.Fatalf("8 comparable: %+v", e)
	}
	// Replies are a different pool: eight posts do not rank a reply.
	reply := seed(t, st, "a", "reply", 1, 1, 0, []int{6}, 6*time.Hour)
	if e = episodeOf(t, st, "a", reply); e.Band != store.BandUnranked {
		t.Fatalf("lone reply: %+v", e)
	}
}

// The window is the completed metric target. A sample taken 9h after posting
// completes the 1h and 6h targets together: the episode is at 6h, it is ranked
// against other 6h samples, and the 24h target stays open.
func TestEpisodeWindowComesFromMetricJobs(t *testing.T) {
	st := openTestStore(t)
	insertTestAgent(t, st, "a")
	seedLikes(t, st, "a", 1, 2, 3, 4, 5, 6, 7)
	late := seed(t, st, "a", "post", 1000, 50, 0, []int{1, 6}, 9*time.Hour)
	e := episodeOf(t, st, "a", late)
	if e.Window != 6 || e.Band != store.BandTop || e.Pool != 8 || e.MeasuredAt.Sub(e.PostedAt) != 9*time.Hour {
		t.Fatalf("late sample: %+v", e)
	}
	// A 1h-only action is ranked among 1h samples (the late sample counts as one), not against the 6h pool.
	for range 7 {
		seed(t, st, "a", "post", 1, 1, 0, []int{1}, time.Hour)
	}
	early := seed(t, st, "a", "post", 1, 9, 0, []int{1}, time.Hour)
	if e = episodeOf(t, st, "a", early); e.Window != 1 || e.Band != store.BandTop || e.Pool != 9 {
		t.Fatalf("1h: %+v", e)
	}
	// A target ended without a sample (media gone) is not a window.
	gone := seed(t, st, "a", "post", 5, 5, 0, nil, time.Hour)
	if _, err := st.DB().Exec(`UPDATE metric_jobs SET completed_at='2026-11-20T00:00:00Z',reason='media_gone' WHERE action_id=?`, gone); err != nil {
		t.Fatal(err)
	}
	eps, err := st.Episodes(context.Background(), "a", store.EpisodeQuery{})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range eps {
		if e.ActionID == gone {
			t.Fatalf("an action without a completed sample is an episode: %+v", e)
		}
	}
}

func TestEpisodesFilterAndHelpers(t *testing.T) {
	st := openTestStore(t)
	insertTestAgent(t, st, "a")
	insertTestAgent(t, st, "b")
	ids := seedLikes(t, st, "a", 5, 3, 8, 1, 9, 0, 7, 2, 6, 4)
	seedLikes(t, st, "b", 1, 2, 3, 4, 5, 6, 7, 8)
	seed(t, st, "a", "post", 1, 1, 0, []int{6}, 6*time.Hour)
	if _, err := st.DB().Exec(`INSERT INTO actions(agent_id,kind,text,dry_run,posted_at,thread_url,media_id) VALUES('a','post','dry',1,'2026-11-01T00:00:00Z','u','m'),('a','post','failed',0,'2026-11-01T00:00:00Z','u','m')`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`UPDATE actions SET failed=1 WHERE text='failed'`); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	all, err := st.Episodes(ctx, "a", store.EpisodeQuery{})
	if err != nil || len(all) != 11 || all[0].ActionID < all[1].ActionID {
		t.Fatalf("agent a has %d episodes newest first, %v", len(all), err)
	}
	newest, _ := st.Episodes(ctx, "a", store.EpisodeQuery{Limit: 3})
	since, _ := st.Episodes(ctx, "a", store.EpisodeQuery{Since: epoch.Add(time.Duration(ids[8]) * time.Hour)})
	replies, _ := st.Episodes(ctx, "a", store.EpisodeQuery{Kind: "reply"})
	if len(newest) != 3 || len(since) != 3 || len(replies) != 0 {
		t.Fatalf("limit %d since %d kind %d", len(newest), len(since), len(replies))
	}
	best, worst := store.Best(all, 2), store.Worst(all, 2)
	if len(best) != 2 || best[0].Score != 9 || best[0].Rank < best[1].Rank || len(worst) != 2 || worst[0].Score > 1 || worst[0].Rank > worst[1].Rank {
		t.Fatalf("best %+v worst %+v", best, worst)
	}
}
