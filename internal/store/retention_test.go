package store_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/store"
)

var retentionNow = time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)

func retentionStore(t *testing.T) (*store.Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "retention.db")
	s, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	for _, id := range []string{"a", "b"} {
		if err = s.EnsureAgent(context.Background(), id); err != nil {
			t.Fatal(err)
		}
	}
	return s, path
}

func stamp(at time.Time) string { return at.UTC().Format(time.RFC3339Nano) }

func prune(t *testing.T, s *store.Store) map[string]int64 {
	t.Helper()
	counts, err := s.Prune(context.Background(), retentionNow)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]int64{}
	for _, c := range counts {
		out[c.Table] = c.Deleted
	}
	return out
}

type ageCase struct {
	name, insert string // insert takes (agent, timestamp)
	count        string // counts rows of agent ?
	age          time.Duration
}

var ageCases = []ageCase{
	{"trend_stories", `INSERT INTO trend_stories(agent_id,observed_at,source,title,url) VALUES(?,?,'s','t','u')`, `SELECT COUNT(*) FROM trend_stories WHERE agent_id=?`, 7 * 24 * time.Hour},
	{"tool_calls", `INSERT INTO tool_calls(agent_id,wake_id,tool,args_json,at) VALUES(?,1,'t','{}',?)`, `SELECT COUNT(*) FROM tool_calls WHERE agent_id=?`, 90 * 24 * time.Hour},
	{"wakes", `INSERT INTO wakes(agent_id,started_at,ended_at) VALUES(?,'2000-01-01T00:00:00Z',?)`, `SELECT COUNT(*) FROM wakes WHERE agent_id=?`, 90 * 24 * time.Hour},
	{"owner_msgs", `INSERT INTO owner_msgs(agent_id,direction,text,at) VALUES(?,'in','x',?)`, `SELECT COUNT(*) FROM owner_msgs WHERE agent_id=?`, 90 * 24 * time.Hour},
	{"metric_jobs", `INSERT INTO metric_jobs(agent_id,action_id,window_hours,due_at,not_before,completed_at) VALUES(?,(SELECT COUNT(*)+1 FROM metric_jobs),1,'2000-01-01T00:00:00Z','2000-01-01T00:00:00Z',?)`, `SELECT COUNT(*) FROM metric_jobs WHERE agent_id=?`, 30 * 24 * time.Hour},
	{"webhook_events", `INSERT INTO webhook_events(agent_id,field,object_id,at) VALUES(?,'replies',(SELECT COUNT(*)+1 FROM webhook_events),?)`, `SELECT COUNT(*) FROM webhook_events WHERE agent_id=?`, 30 * 24 * time.Hour},
}

func insertAged(t *testing.T, s *store.Store, c ageCase, agent string, at time.Time) {
	t.Helper()
	exec(t, s, c.insert, agent, stamp(at))
}

func TestPruneAgeBoundaryPerTable(t *testing.T) {
	for _, c := range ageCases {
		t.Run(c.name, func(t *testing.T) {
			s, _ := retentionStore(t)
			boundary := retentionNow.Add(-c.age)
			insertAged(t, s, c, "a", boundary)
			insertAged(t, s, c, "a", boundary.Add(-time.Second))
			insertAged(t, s, c, "b", boundary.Add(-time.Second))
			deleted := prune(t, s)
			if got := countRows(t, s, c.count, "a"); got != 1 {
				t.Fatalf("agent a keeps %d rows, want only the boundary row", got)
			}
			if got := countRows(t, s, c.count, "b"); got != 0 {
				t.Fatalf("agent b keeps %d rows past the boundary", got)
			}
			if deleted[c.name] != 2 {
				t.Fatalf("reported %d deleted, want 2", deleted[c.name])
			}
		})
	}
}

func TestPruneKeepsRowsWithoutAgeColumn(t *testing.T) {
	s, _ := retentionStore(t)
	exec(t, s, `INSERT INTO wakes(agent_id,started_at) VALUES('a','2000-01-01T00:00:00Z')`)
	exec(t, s, `INSERT INTO metric_jobs(agent_id,action_id,window_hours,due_at,not_before) VALUES('a',1,1,'2000-01-01T00:00:00Z','2000-01-01T00:00:00Z')`)
	prune(t, s)
	if countRows(t, s, `SELECT COUNT(*) FROM wakes`) != 1 || countRows(t, s, `SELECT COUNT(*) FROM metric_jobs`) != 1 {
		t.Fatal("pruned a running wake or an uncompleted job")
	}
}

func TestPruneTrendStoriesRowCapPerAgent(t *testing.T) {
	s, _ := retentionStore(t)
	insert := func(agent string, n int) {
		for i := 0; i < n; i++ {
			exec(t, s, `INSERT INTO trend_stories(agent_id,observed_at,source,title,url) VALUES(?,?,'s',?,'u')`, agent, stamp(retentionNow.Add(-time.Duration(i)*time.Second)), fmt.Sprint(i))
		}
	}
	insert("a", 503)
	insert("b", 500)
	prune(t, s)
	if got := countRows(t, s, `SELECT COUNT(*) FROM trend_stories WHERE agent_id='a'`); got != 500 {
		t.Fatalf("agent a keeps %d rows, want 500", got)
	}
	if got := countRows(t, s, `SELECT COUNT(*) FROM trend_stories WHERE agent_id='b'`); got != 500 {
		t.Fatalf("agent b keeps %d rows, want 500", got)
	}
	if got := countRows(t, s, `SELECT COUNT(*) FROM trend_stories WHERE agent_id='a' AND title IN ('500','501','502')`); got != 0 {
		t.Fatal("kept older rows over newer ones")
	}
}

func TestPruneContacts(t *testing.T) {
	s, _ := retentionStore(t)
	window := retentionNow.Add(-7 * 24 * time.Hour)
	reply := func(agent, user string, at time.Time) {
		exec(t, s, `INSERT INTO actions(agent_id,kind,text,target_username,posted_at) VALUES(?,'reply','',?,?)`, agent, user, stamp(at))
	}
	contact := func(agent, user string, dnc int) {
		exec(t, s, `INSERT INTO contacts(agent_id,username,reply_count_7d,do_not_contact) VALUES(?,?,1,?)`, agent, user, dnc)
	}
	for _, c := range []struct {
		agent, user string
		dnc         int
	}{{"a", "edge", 0}, {"a", "past", 0}, {"a", "idle", 0}, {"a", "blocked", 1}, {"b", "past", 0}, {"a", "mixed", 0}} {
		contact(c.agent, c.user, c.dnc)
	}
	reply("a", "edge", window)
	reply("a", "past", window.Add(-time.Second))
	reply("a", "blocked", window.Add(-time.Hour))
	reply("a", "@Mixed", window.Add(-time.Hour))
	reply("a", " MIXED ", retentionNow)
	reply("b", "edge", window)
	deleted := prune(t, s)
	left := map[string]bool{}
	rows, err := s.DB().Query(`SELECT agent_id||'/'||username FROM contacts`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		if err = rows.Scan(&k); err != nil {
			t.Fatal(err)
		}
		left[k] = true
	}
	want := map[string]bool{"a/edge": true, "a/blocked": true, "a/mixed": true}
	if len(left) != len(want) {
		t.Fatalf("contacts left %v, want %v", left, want)
	}
	for k := range want {
		if !left[k] {
			t.Fatalf("contacts left %v, want %v", left, want)
		}
	}
	if deleted["contacts"] != 3 {
		t.Fatalf("reported %d contacts deleted, want 3", deleted["contacts"])
	}
}

func TestPruneIsIdempotent(t *testing.T) {
	s, _ := retentionStore(t)
	insertAged(t, s, ageCases[1], "a", retentionNow.Add(-91*24*time.Hour))
	if prune(t, s)["tool_calls"] != 1 || prune(t, s)["tool_calls"] != 0 {
		t.Fatal("second prune deleted rows")
	}
}

func TestFreshDatabaseIsIncrementalVacuumAndPruneShrinksFile(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "vacuum.db")
	s, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var mode int
	if err = s.DB().QueryRow(`PRAGMA auto_vacuum`).Scan(&mode); err != nil || mode != 2 {
		t.Fatalf("auto_vacuum %d %v, want incremental (2)", mode, err)
	}
	if err = s.EnsureAgent(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	blob := strings.Repeat("x", 4000)
	for i := 0; i < 500; i++ {
		exec(t, s, `INSERT INTO owner_msgs(agent_id,direction,text,at) VALUES('a','in',?,?)`, blob, stamp(retentionNow.Add(-100*24*time.Hour)))
	}
	before := fileSize(t, path)
	if got := prune(t, s)["owner_msgs"]; got != 500 {
		t.Fatalf("deleted %d", got)
	}
	if after := fileSize(t, path); after >= before/2 {
		t.Fatalf("file %d -> %d bytes, want freed pages returned", before, after)
	}
	if free := countRows(t, s, `PRAGMA freelist_count`); free != 0 {
		t.Fatalf("%d free pages left", free)
	}
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Size()
}

func openRaw(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	if _, err = db.Exec(`CREATE TABLE seed(a)`); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func TestSeedMetricJobsSkipsActionsBeyondTheJobRetention(t *testing.T) {
	s, _ := retentionStore(t)
	ctx := context.Background()
	edge := retentionNow.Add(-29 * 24 * time.Hour)
	exec(t, s, `INSERT INTO actions(agent_id,kind,text,posted_at,thread_url,dry_run,media_id) VALUES('a','post','',?,'https://example.test/x',0,'m')`, stamp(edge))
	exec(t, s, `INSERT INTO actions(agent_id,kind,text,posted_at,thread_url,dry_run,media_id) VALUES('a','post','',?,'https://example.test/y',0,'m')`, stamp(edge.Add(-time.Second)))
	if n, err := s.SeedMetricJobs(ctx, "a", retentionNow); err != nil || n != 3 {
		t.Fatalf("seeded %d jobs, err %v, want 3 for the action inside the window", n, err)
	}
	if n, err := s.SeedMetricJobs(ctx, "a", retentionNow.Add(time.Second)); err != nil || n != 0 {
		t.Fatalf("seeded %d jobs past the window, err %v", n, err)
	}
}

func TestOpenRefusesADatabaseOfAnotherSchemaVersion(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "old.db")
	raw, err := openRaw(path)
	if err != nil {
		t.Fatal(err)
	}
	raw.Close()
	_, err = store.Open(ctx, path)
	if err == nil || !strings.Contains(err.Error(), "start a fresh database") {
		t.Fatalf("old database accepted or unclear: %v", err)
	}
}
