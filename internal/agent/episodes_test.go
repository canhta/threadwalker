package agent_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/store"
)

// seedEpisode inserts a live confirmed action posted at posted and measured at
// the given window, so it is an episode of that window.
func seedEpisode(t *testing.T, st *store.Store, agentID, kind, text string, posted time.Time, window int, views, likes, replies int64) int64 {
	t.Helper()
	db := st.DB()
	var parentReplies, parentAge any // only a reply has a target
	if kind == "reply" {
		parentReplies, parentAge = 4, 30
	}
	res, err := db.Exec(`INSERT INTO actions(agent_id,kind,text,dry_run,posted_at,thread_url,media_id,hook,angle,fact,parent_replies,parent_age_minutes) VALUES(?,?,?,0,?,'https://threads.test/p','m','number','angle text','fact text',?,?)`,
		agentID, kind, text, posted.Format(time.RFC3339Nano), parentReplies, parentAge)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	measured := posted.Add(time.Duration(window) * time.Hour).Format(time.RFC3339Nano)
	if _, err = db.Exec(`INSERT INTO metrics(agent_id,action_id,measured_at,views,likes,replies) VALUES(?,?,?,?,?,?)`, agentID, id, measured, views, likes, replies); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO metric_jobs(agent_id,action_id,window_hours,due_at,not_before,completed_at) VALUES(?,?,?,?,?,?)`, agentID, id, window, measured, measured, measured); err != nil {
		t.Fatal(err)
	}
	return id
}

func seedLesson(t *testing.T, st *store.Store, agentID, kind, polarity, text string, created, confirmed time.Time) int64 {
	t.Helper()
	res, err := st.DB().Exec(`INSERT INTO lessons(agent_id,kind,polarity,text,evidence_json,created_at,confirmed_at) VALUES(?,?,?,?,'[]',?,?)`, agentID, kind, polarity, text, created.Format(time.RFC3339Nano), confirmed.Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

// firstJSON decodes the JSON value that opens a wake's user message; the
// scoreboard, inbox and notes follow it as text.
func firstJSON(t *testing.T, content string, into any) {
	t.Helper()
	if err := json.NewDecoder(strings.NewReader(content)).Decode(into); err != nil {
		t.Fatalf("user message does not open with JSON: %v\n%.300s", err, content)
	}
}
