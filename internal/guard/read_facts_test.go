package guard_test

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

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
	facts, err := trends.PostFacts(threads.Post{Username: "fixture_author", Text: "t", URL: target, Likes: n64(120), Replies: n64(8), AgeMinutes: n64(45)}, config.DefaultTargets)
	if err != nil || facts.Line == "" {
		t.Fatalf("%+v %v", facts, err)
	}
	g.NoteFacts(target, facts)
	var id int64
	// The permalink arrives with a query.
	ref := readPost(t, g, target+"?x=1", "tid", "")
	if _, err := g.Execute(context.Background(), guard.Attempt{Tool: "reply", Arguments: `{"text":"Synthetic reply","target":"` + ref + `"}`, ReservedActionID: &id}, func() (string, error) { return "", nil }); err != nil {
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
	if seen[0].Ref != "t4" || seen[0].Facts != "[target score 0.90]" || seen[1].Score != 0.3 || seen[2].Facts != "" || seen[2].Ref != "t1" {
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
	reply := func(g *guard.Guard, ref string) error {
		var id int64
		_, err := g.Execute(context.Background(), guard.Attempt{Tool: "reply", Arguments: `{"text":"Synthetic reply","target":"` + ref + `"}`, ReservedActionID: &id}, func() (string, error) { return "", nil })
		return err
	}
	dead := "https://threads.com/@fixture_author/post/dead1"
	facts, err := trends.PostFacts(threads.Post{Username: "fixture_author", Text: "t", URL: dead, Likes: n64(1), Replies: n64(0), AgeMinutes: n64(60)}, opts.Template.Targets)
	if err != nil || facts.Outside == "" {
		t.Fatalf("%+v %v", facts, err)
	}

	g, err := guard.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	ref := readPost(t, g, dead, "tid", "")
	g.NoteFacts(dead, facts)
	b, hint := modelResult(t, reply(g, ref))
	if b != "target_outside_window" || !strings.Contains(hint, "likes_per_hour 1 < min_likes_per_hour 5") {
		t.Fatalf("%q %q", b, hint)
	}

	g, err = guard.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	ref = g.NoteInbox(context.Background(), threads.Post{ID: "tid", URL: dead}, "")
	g.NoteFacts(dead, facts)
	if err := reply(g, ref); err != nil {
		t.Fatalf("inbox reply: %v", err)
	}
}
