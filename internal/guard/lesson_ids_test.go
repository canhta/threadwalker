package guard_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/canhta/threadz/internal/guard"
)

func TestReplyStoresBriefAndAppliedLessonsTogether(t *testing.T) {
	st, opts := setup(t)
	opts.Template.Limits.PostsPerDay = 5
	opts.Template.Limits.RepliesPerDay = 5
	opts.Template.Limits.RepliesPerUser7d = 5
	opts.Template.Limits.MinSecondsBetweenActions = 1
	g, err := guard.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []string{
		`('agent','reply','do','active reply','[]','2026-11-01T00:00:00Z','2026-11-01T00:00:00Z',NULL,NULL)`,
		`('agent','any','avoid','active any','[]','2026-11-01T00:00:00Z','2026-11-01T00:00:00Z',NULL,NULL)`,
		`('agent','post','do','active post','[]','2026-11-01T00:00:00Z','2026-11-01T00:00:00Z',NULL,NULL)`,
		`('agent','reply','do','retired','[]','2026-11-01T00:00:00Z','2026-11-01T00:00:00Z','2026-11-02T00:00:00Z','dream')`,
		`('other','reply','do','foreign','[]','2026-11-01T00:00:00Z','2026-11-01T00:00:00Z',NULL,NULL)`,
	} {
		if _, err = st.DB().Exec(`INSERT INTO lessons(agent_id,kind,polarity,text,evidence_json,created_at,confirmed_at,retired_at,retired_by) VALUES ` + row); err != nil {
			t.Fatal(err)
		}
	}
	const target = "https://threads.com/@fixture_author/post/1"
	reply := func(ids string) (int64, error) {
		var id int64
		_, err := g.Execute(context.Background(), guard.Attempt{Tool: "reply", ReservedActionID: &id, Arguments: `{"text":"Synthetic reply","target":"` + readPost(t, g, target, "tid", "") + `","angle":"the point","fact":"the detail"` + ids + `}`}, func() (string, error) { return "", nil })
		return id, err
	}
	for _, tc := range []struct{ name, ids string }{
		{"wrong kind", `,"lesson_ids":[3]`},
		{"retired", `,"lesson_ids":[4]`},
		{"another agent's", `,"lesson_ids":[5]`},
		{"missing", `,"lesson_ids":[99]`},
		{"one bad among good", `,"lesson_ids":[1,4]`},
		{"repeated", `,"lesson_ids":[1,1]`},
		{"four", `,"lesson_ids":[1,2,6,7]`},
	} {
		_, err := reply(tc.ids)
		var blocked *guard.BlockedError
		if !errors.As(err, &blocked) || blocked.Reason != "invalid_lesson_ids" || guard.Hint(blocked.Reason, "") == "" {
			t.Errorf("%s: %v, want invalid_lesson_ids", tc.name, err)
		}
	}
	var actions, applied int
	if err = st.DB().QueryRow(`SELECT (SELECT COUNT(*) FROM actions),(SELECT COUNT(*) FROM action_lessons)`).Scan(&actions, &applied); err != nil || actions != 0 || applied != 0 {
		t.Fatalf("refusals left %d actions, %d applied lessons, %v", actions, applied, err)
	}

	id, err := reply(`,"lesson_ids":[1,2]`)
	if err != nil {
		t.Fatal(err)
	}
	var angle, fact string
	var got []int64
	if err = st.DB().QueryRow(`SELECT angle,fact FROM actions WHERE id=?`, id).Scan(&angle, &fact); err != nil || angle != "the point" || fact != "the detail" {
		t.Fatalf("brief %q %q, %v", angle, fact, err)
	}
	rows, err := st.DB().Query(`SELECT lesson_id FROM action_lessons WHERE action_id=? ORDER BY lesson_id`, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var l int64
		if err = rows.Scan(&l); err != nil {
			t.Fatal(err)
		}
		got = append(got, l)
	}
	if fmt.Sprint(got) != "[1 2]" {
		t.Fatalf("applied lessons %v", got)
	}
}

// A failure writing the applied lessons takes the action with it.
func TestReplyAndAppliedLessonsCommitTogether(t *testing.T) {
	st, opts := setup(t)
	opts.Template.Limits.RepliesPerDay = 5
	opts.Template.Limits.PostsPerDay = 5
	opts.Template.Limits.RepliesPerUser7d = 5
	opts.Template.Limits.MinSecondsBetweenActions = 1
	g, _ := guard.New(opts)
	if _, err := st.DB().Exec(`INSERT INTO lessons(agent_id,kind,polarity,text,evidence_json,created_at,confirmed_at) VALUES('agent','any','do','x','[]','2026-11-01T00:00:00Z','2026-11-01T00:00:00Z');
		CREATE TRIGGER no_applied BEFORE INSERT ON action_lessons BEGIN SELECT RAISE(FAIL,'disk'); END`); err != nil {
		t.Fatal(err)
	}
	const target = "https://threads.com/@fixture_author/post/1"
	_, err := g.Execute(context.Background(), guard.Attempt{Tool: "reply", Arguments: `{"text":"Synthetic reply","target":"` + readPost(t, g, target, "tid", "") + `","lesson_ids":[1]}`}, func() (string, error) { return "", nil })
	if err == nil {
		t.Fatal("reply succeeded")
	}
	var n int
	if err = st.DB().QueryRow(`SELECT COUNT(*) FROM actions`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("%d actions left behind, %v", n, err)
	}
}
