package guard_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/guard"
	"github.com/canhta/threadz/internal/threads"
	"github.com/canhta/threadz/internal/trends"
)

func TestReplyRecordsTheFiguresOfTheTargetItRead(t *testing.T) {
	st, opts := setup(t)
	opts.Template.Limits.PostsPerDay = 2
	opts.Template.Limits.RepliesPerDay = 2
	opts.Template.Limits.RepliesPerUser7d = 2
	opts.Template.Limits.MinSecondsBetweenActions = 1
	g, err := guard.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	reply := func(url string) int64 {
		var id int64
		g.NoteRead("tid", url, "")
		if _, err := g.Execute(context.Background(), guard.Attempt{Tool: "reply", Arguments: `{"text":"Synthetic reply","target_username":"fixture_author","target_id": "tid", "target_url":"` + url + `"}`, ReservedActionID: &id}, func() (string, error) { return "", nil }); err != nil {
			t.Fatal(err)
		}
		return id
	}
	target := "https://threads.com/@fixture_author/post/1"
	age, replies, likes := int64(50), int64(11), int64(60)
	facts, err := trends.PostFacts(threads.Post{AgeMinutes: &age, Replies: &replies, Likes: &likes}, config.DefaultTargets)
	if err != nil {
		t.Fatal(err)
	}
	g.NoteFacts(target, facts)
	var scoreJSON string
	var parentReplies, parentAge sql.NullInt64
	id := reply(target + "?tracking=x")
	if err := st.DB().QueryRow(`SELECT target_score_json,parent_replies,parent_age_minutes FROM actions WHERE id=?`, id).Scan(&scoreJSON, &parentReplies, &parentAge); err != nil {
		t.Fatal(err)
	}
	if scoreJSON == "" || !parentReplies.Valid || parentReplies.Int64 != 11 || !parentAge.Valid || parentAge.Int64 != 50 {
		t.Fatalf("read target: %q %+v %+v", scoreJSON, parentReplies, parentAge)
	}
	for _, want := range []string{`"age_minutes":50`, `"replies":11`, `"likes":60`} {
		if !strings.Contains(scoreJSON, want) {
			t.Errorf("%s missing %s", scoreJSON, want)
		}
	}
	now := opts.Now()
	opts.Now = func() time.Time { return now.Add(time.Hour) }
	if g, err = guard.New(opts); err != nil {
		t.Fatal(err)
	}
	id = reply("https://threads.com/@fixture_author/post/unread")
	if err := st.DB().QueryRow(`SELECT target_score_json,parent_replies,parent_age_minutes FROM actions WHERE id=?`, id).Scan(&scoreJSON, &parentReplies, &parentAge); err != nil || scoreJSON != "" || parentReplies.Valid || parentAge.Valid {
		t.Fatalf("a target whose figures were not read must stay unscored: %q %+v %+v %v", scoreJSON, parentReplies, parentAge, err)
	}
}
