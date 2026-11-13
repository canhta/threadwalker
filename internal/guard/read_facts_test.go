package guard_test

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/guard"
	"github.com/canhta/threadz/internal/threads"
	"github.com/canhta/threadz/internal/trends"
)

func n64(v int64) *int64 { return &v }

func TestActionStoresFeaturesOfWhatTheWakeRead(t *testing.T) {
	st, opts := setup(t)
	opts.Template.Limits.PostsPerDay = 2
	opts.Template.Limits.RepliesPerDay = 2
	opts.Template.Limits.RepliesPerUser7d = 2
	opts.Template.Limits.MinSecondsBetweenActions = 1
	g, err := guard.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	target := "https://threads.com/@fixture_author/post/read1"
	facts, err := trends.PostFacts(threads.Post{Username: "fixture_author", Text: "t", URL: target, Likes: n64(120), Replies: n64(8), AgeMinutes: n64(45)}, nil, config.DefaultTargets)
	if err != nil || facts.Line == "" {
		t.Fatalf("%+v %v", facts, err)
	}
	g.NoteFacts(target, facts)
	now := opts.Now()
	topic := "https://www.threads.com/search?q=x&serp_type=trends&trend_fbid=1"

	var id int64
	g.NoteRead("tid", target+"?x=1", "")
	// The permalink arrives with a query.
	if _, err := g.Execute(context.Background(), guard.Attempt{Tool: "reply", Arguments: `{"text":"Synthetic reply","target_username":"fixture_author","target_id":"tid","target_url":"` + target + `?x=1"}`, ReservedActionID: &id}, func() (string, error) { return "", nil }); err != nil {
		t.Fatal(err)
	}
	var scoreJSON string
	var replies, age sql.NullInt64
	if err := st.DB().QueryRow(`SELECT target_score_json,parent_replies,parent_age_minutes FROM actions WHERE id=?`, id).Scan(&scoreJSON, &replies, &age); err != nil {
		t.Fatal(err)
	}
	if !replies.Valid || replies.Int64 != 8 || !age.Valid || age.Int64 != 45 {
		t.Fatalf("parent figures: %+v %+v", replies, age)
	}
	for _, want := range []string{`"likes":120`, `"likes_per_hour":160`, `"age_minutes":45`} {
		if !strings.Contains(scoreJSON, want) {
			t.Errorf("%s missing %s", scoreJSON, want)
		}
	}

	opts.Now = func() time.Time { return now.Add(time.Hour) }
	if g, err = guard.New(opts); err != nil {
		t.Fatal(err)
	}
	g.NoteTrendText(topic, "topic")
	g.NoteFacts(topic, trends.TopicFacts(trends.Heat{Posts: n64(2000)}, now))
	var post int64
	if _, err := g.Execute(context.Background(), guard.Attempt{Tool: "post", Arguments: `{"text":"Synthetic post","trend_ref":"` + topic + `"}`, ReservedActionID: &post}, func() (string, error) { return "", nil }); err != nil {
		t.Fatal(err)
	}
	var heat sql.NullFloat64
	if err := st.DB().QueryRow(`SELECT topic_heat FROM actions WHERE id=?`, post).Scan(&heat); err != nil || !heat.Valid || heat.Float64 <= 0 {
		t.Fatalf("topic heat %+v %v", heat, err)
	}
}

func TestSeenListsBestFirstWithFactsAndCaps(t *testing.T) {
	_, opts := setup(t)
	g, err := guard.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < guard.MaxSeen+5; i++ {
		g.NoteTrendText(fmt.Sprintf("https://www.threads.com/@a/post/p%02d", i), "text")
	}
	hot := "https://www.threads.com/@a/post/p03"
	g.NoteFacts(hot, trends.ItemFacts{Line: "[target score 0.90]", Score: 0.9})
	g.NoteFacts("https://www.threads.com/@a/post/p04", trends.ItemFacts{Line: "[target score 0.30]", Score: 0.3})
	seen := g.Seen()
	if len(seen) != guard.MaxSeen {
		t.Fatalf("%d items", len(seen))
	}
	if seen[0].Ref != "r4" || seen[0].Facts != "[target score 0.90]" || seen[1].Score != 0.3 || seen[2].Facts != "" || seen[2].Ref != "r1" {
		t.Fatalf("order: %+v", seen[:3])
	}
}

func TestReplyToAnOutsideWindowTargetIsRefusedUnlessInbox(t *testing.T) {
	_, opts := setup(t)
	opts.Template.Limits.PostsPerDay = 5
	opts.Template.Limits.RepliesPerDay = 5
	opts.Template.Limits.RepliesPerUser7d = 5
	opts.Template.Limits.MinSecondsBetweenActions = 1
	opts.Template.Targets = config.DefaultTargets
	reply := func(g *guard.Guard, url string) error {
		var id int64
		_, err := g.Execute(context.Background(), guard.Attempt{Tool: "reply", Arguments: `{"text":"Synthetic reply","target_username":"fixture_author","target_id":"tid","target_url":"` + url + `"}`, ReservedActionID: &id}, func() (string, error) { return "", nil })
		return err
	}
	dead := "https://threads.com/@fixture_author/post/dead1"
	facts, err := trends.PostFacts(threads.Post{Username: "fixture_author", Text: "t", URL: dead, Likes: n64(1), Replies: n64(0), AgeMinutes: n64(60)}, nil, opts.Template.Targets)
	if err != nil || facts.Outside == "" {
		t.Fatalf("%+v %v", facts, err)
	}

	g, err := guard.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	g.NoteRead("tid", dead, "")
	g.NoteFacts(dead, facts)
	b, hint := modelResult(t, reply(g, dead))
	if b != "target_outside_window" || !strings.Contains(hint, "likes_per_hour 1 < min_likes_per_hour 5") {
		t.Fatalf("%q %q", b, hint)
	}

	g, err = guard.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	g.NoteInbox("tid", dead, "", "")
	g.NoteFacts(dead, facts)
	if err := reply(g, dead); err != nil {
		t.Fatalf("inbox reply: %v", err)
	}
}
