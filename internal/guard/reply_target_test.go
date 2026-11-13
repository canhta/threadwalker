package guard_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/guard"
	"github.com/canhta/threadz/internal/store"
)

func TestReplyRecordsTargetScoreAndDelay(t *testing.T) {
	st, opts := setup(t)
	opts.Template.Limits.PostsPerDay = 2
	opts.Template.Limits.RepliesPerDay = 2
	opts.Template.Limits.RepliesPerUser7d = 2
	opts.Template.Limits.MinSecondsBetweenActions = 1
	now := opts.Now()
	hash := sha256.Sum256([]byte("snippet"))
	count := func(v int64) *int64 { return &v }
	observe := func(url string, replies int64, ago time.Duration) store.ThreadsObservation {
		return store.ThreadsObservation{PostURL: url, Author: "fixture_author", TextHash: hex.EncodeToString(hash[:]), Snippet: "synthetic hot take", Replies: count(replies), Kind: "feed", ObservedAt: now.Add(-ago)}
	}
	target := "https://threads.com/@fixture_author/post/1"
	err := st.RecordThreadsObservations(context.Background(), "agent", []store.ThreadsObservation{observe(target, 1, 50*time.Minute), observe(target, 11, 10*time.Minute)}, now)
	if err != nil {
		t.Fatal(err)
	}
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
	var scoreJSON string
	var delay sql.NullInt64
	id := reply(target + "?tracking=x")
	if err := st.DB().QueryRow(`SELECT target_score_json,delay_minutes FROM actions WHERE id=?`, id).Scan(&scoreJSON, &delay); err != nil {
		t.Fatal(err)
	}
	if !delay.Valid || delay.Int64 != 50 || scoreJSON == "" {
		t.Fatalf("observed target: %q %+v", scoreJSON, delay)
	}
	for _, want := range []string{`"reply_velocity":15`, `"age_minutes":50`, `"already_replied":false`, `"joke_saturation":0`} {
		if !strings.Contains(scoreJSON, want) {
			t.Errorf("%s missing %s", scoreJSON, want)
		}
	}
	opts.Now = func() time.Time { return now.Add(time.Hour) }
	if g, err = guard.New(opts); err != nil {
		t.Fatal(err)
	}
	id = reply("https://threads.com/@fixture_author/post/unobserved")
	if err := st.DB().QueryRow(`SELECT target_score_json,delay_minutes FROM actions WHERE id=?`, id).Scan(&scoreJSON, &delay); err != nil || scoreJSON != "" || delay.Valid {
		t.Fatalf("unobserved target must stay unscored: %q %+v %v", scoreJSON, delay, err)
	}
}
