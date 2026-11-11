package playbook_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/canhta/threadwalker/internal/config"
	"github.com/canhta/threadwalker/internal/guard"
	"github.com/canhta/threadwalker/internal/logging"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/canhta/threadwalker/internal/playbook"
	"github.com/canhta/threadwalker/internal/store"
)

func TestMetricOutcomesRequireFrozenRevisionAndCompleted24HourTarget(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "playbook.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err = st.EnsureAgent(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	if _, err = st.InitializePlaybook(ctx, "a", []string{"idea"}, now); err != nil {
		t.Fatal(err)
	}
	if _, err = st.DB().Exec(`INSERT INTO actions(id,agent_id,kind,text,dry_run,posted_at,thread_url) VALUES(1,'a','post','fixture',0,?,'https://fixture.invalid/1'); INSERT INTO metrics(agent_id,action_id,measured_at,views,likes) VALUES('a',1,?,100,90)`, now.Format(time.RFC3339Nano), now.Add(24*time.Hour).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	// A raw metric row and a model-supplied hypothesis tag cannot create evidence.
	if err = playbook.Process(ctx, st, "a", now.Add(24*time.Hour), nil); err != nil {
		t.Fatal(err)
	}
	entries, err := st.PlaybookEntries(ctx, "a", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].N != 0 || entries[0].Successes != 0 {
		t.Fatalf("entries=%+v", entries)
	}
}

// syntheticControls supplies mature untagged observations, never live evidence.
func syntheticControls(t *testing.T, st *store.Store, agentID string, now time.Time) {
	t.Helper()
	ctx := context.Background()
	tx, err := st.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for i := 0; i < 300; i++ {
		posted := now.Add(-72*time.Hour + time.Duration(i)*time.Minute)
		at := posted.Add(24 * time.Hour)
		likes := int64(10)
		if i >= 100 && i < 200 {
			likes = 5
		}
		if i >= 200 {
			likes = 20
		}
		r, e := tx.Exec(`INSERT INTO actions(agent_id,kind,text,dry_run,posted_at,thread_url) VALUES(?,'post','PRIVATE CONTROL',0,?,'https://fixture.invalid/control')`, agentID, posted.Format(time.RFC3339Nano))
		if e != nil {
			t.Fatal(e)
		}
		id, e := r.LastInsertId()
		if e != nil {
			t.Fatal(e)
		}
		if _, e = tx.Exec(`INSERT INTO metrics(agent_id,action_id,measured_at,views,likes) VALUES(?,?,?,100,?)`, agentID, id, at.Format(time.RFC3339Nano), likes); e != nil {
			t.Fatal(e)
		}
		for _, h := range []int{1, 6, 24} {
			if _, e = tx.Exec(`INSERT INTO metric_jobs(agent_id,action_id,window_hours,due_at,not_before,completed_at) VALUES(?,?,?,?,?,?)`, agentID, id, h, posted.Add(time.Duration(h)*time.Hour).Format("2006-01-02T15:04:05.000000000Z"), posted.Add(time.Duration(h)*time.Hour).Format("2006-01-02T15:04:05.000000000Z"), at.Format(time.RFC3339Nano)); e != nil {
				t.Fatal(e)
			}
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func reserveAction(t *testing.T, st *store.Store, agentID string, hypothesisID int64, at time.Time) int64 {
	t.Helper()
	ctx := context.Background()
	tmpl := config.Template{ID: agentID, DryRun: false, Limits: config.LimitsConfig{PostsPerDay: 2000, RepliesPerDay: 2000, RepliesPerUser7d: 100, MinSecondsBetweenActions: 1, MinMinutesBetweenWakes: 1}}
	var wakeID int64
	if err := st.DB().QueryRow(`SELECT id FROM wakes WHERE agent_id=? AND ended_at IS NULL`, agentID).Scan(&wakeID); err != nil {
		var e error
		wakeID, e = st.BeginWake(ctx, agentID, at, at.Add(time.Hour))
		if e != nil {
			t.Fatal(e)
		}
	}
	g, err := guard.New(guard.Options{Store: st, Template: tmpl, WakeID: wakeID, AllowLive: true, Now: func() time.Time { return at }})
	if err != nil {
		t.Fatal(err)
	}
	var id int64
	_, err = g.Execute(ctx, guard.Attempt{Tool: "post", Arguments: fmt.Sprintf(`{"text":"PRIVATE CANDIDATE","hypothesis_ids":[%d]}`, hypothesisID), ReservedActionID: &id}, func() (string, error) {
		_, e := st.DB().Exec(`UPDATE actions SET thread_url='https://fixture.invalid/candidate' WHERE id=? AND agent_id=?`, id, agentID)
		return `{}`, e
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func matureActions(t *testing.T, st *store.Store, agentID string, at time.Time, likes int64) {
	t.Helper()
	ctx := context.Background()
	if _, e := st.SeedMetricJobs(ctx, agentID); e != nil {
		t.Fatal(e)
	}
	jobs, e := st.ClaimMetricJobs(ctx, agentID, at, 5*time.Minute, 1000)
	if e != nil {
		t.Fatal(e)
	}
	groups := map[int64][]store.MetricJob{}
	for _, j := range jobs {
		groups[j.ActionID] = append(groups[j.ActionID], j)
	}
	for _, group := range groups {
		if _, e = st.CompleteMetricJobs(ctx, group, at, store.MetricValues{Views: 100, Likes: likes}); e != nil {
			t.Fatal(e)
		}
	}
}

func TestFrozenBaselineCountsOnceAndEstablishesWithMetadataOnlyDecision(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "outcomes.db")
	st, e := store.Open(ctx, path)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { st.Close() }()
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	if e = st.EnsureAgent(ctx, "a"); e != nil {
		t.Fatal(e)
	}
	if e = st.EnsureAgent(ctx, "other"); e != nil {
		t.Fatal(e)
	}
	syntheticControls(t, st, "a", now)
	if _, e = st.InitializePlaybook(ctx, "a", []string{"PRIVATE HYPOTHESIS"}, now); e != nil {
		t.Fatal(e)
	}
	if _, e = st.InitializePlaybook(ctx, "other", []string{"foreign"}, now); e != nil {
		t.Fatal(e)
	}
	entries, e := st.PlaybookEntries(ctx, "a", 100)
	if e != nil {
		t.Fatal(e)
	}
	id := entries[0].ID
	for i := 0; i < 50; i++ {
		reserveAction(t, st, "a", id, now.Add(time.Duration(i)*time.Minute))
	}
	syntheticCurrentControls(t, st, "a", now.Add(10*time.Minute), 200, 10)
	var baselineN, baselineSuccesses int
	var threshold float64
	if e = st.DB().QueryRow(`SELECT n,successes,threshold FROM playbook_baselines WHERE agent_id='a'`).Scan(&baselineN, &baselineSuccesses, &threshold); e != nil || baselineN != 200 || baselineSuccesses != 100 || threshold != 0.1 {
		t.Fatalf("baseline=%d/%d threshold=%v err=%v", baselineSuccesses, baselineN, threshold, e)
	}
	// Immature targets do not count. In particular 1h and 6h successes are not evidence.
	matureActions(t, st, "a", now.Add(7*time.Hour), 90)
	if e = playbook.Process(ctx, st, "a", now.Add(7*time.Hour), nil); e != nil {
		t.Fatal(e)
	}
	entries, e = st.PlaybookEntries(ctx, "a", 100)
	if e != nil || entries[0].N != 0 {
		t.Fatalf("early %+v %v", entries, e)
	}
	matureActions(t, st, "a", now.Add(25*time.Hour), 90)
	// A DB failure must roll back counts and outcome markers together.
	if _, e = st.DB().Exec(`CREATE TRIGGER fail_outcome BEFORE INSERT ON playbook_outcomes BEGIN SELECT RAISE(ABORT,'PRIVATE FAILURE'); END`); e != nil {
		t.Fatal(e)
	}
	if e = playbook.Process(ctx, st, "a", now.Add(25*time.Hour), nil); e == nil {
		t.Fatal("hidden persistence failure")
	}
	entries, e = st.PlaybookEntries(ctx, "a", 100)
	if e != nil || entries[0].N != 0 {
		t.Fatalf("partial counts %+v %v", entries, e)
	}
	if _, e = st.DB().Exec(`DROP TRIGGER fail_outcome`); e != nil {
		t.Fatal(e)
	}
	var logs bytes.Buffer
	logger, _ := logging.New(&logs, "debug", "json")
	if e = playbook.Process(ctx, st, "a", now.Add(25*time.Hour), logger); e != nil {
		t.Fatal(e)
	}
	entries, e = st.PlaybookEntries(ctx, "a", 100)
	if e != nil || entries[0].Status != "established" || entries[0].N != 50 || entries[0].Successes != 50 {
		t.Fatalf("entries=%+v err=%v", entries, e)
	}
	before := logs.String()
	if strings.Contains(before, "PRIVATE") || strings.Contains(before, "https://") || !strings.Contains(before, `"event":"playbook.decided"`) || !strings.Contains(before, `"hypothesis_id":`) {
		t.Fatal(before)
	}
	foreign, e := st.PlaybookEntries(ctx, "other", 100)
	if e != nil || foreign[0].N != 0 {
		t.Fatalf("foreign=%+v %v", foreign, e)
	}
	if e = st.Close(); e != nil {
		t.Fatal(e)
	}
	st, e = store.Open(ctx, path)
	if e != nil {
		t.Fatal(e)
	}
	if e = playbook.Process(ctx, st, "a", now.Add(26*time.Hour), logger); e != nil {
		t.Fatal(e)
	}
	entries, e = st.PlaybookEntries(ctx, "a", 100)
	if e != nil || entries[0].N != 50 || logs.String() != before {
		t.Fatal("restart duplicated evidence or decision")
	}
}

func TestAmendmentAndStaleMetricsCannotInheritEvidence(t *testing.T) {
	for _, mode := range []string{"revision", "late", "zero_views", "no_baseline", "kind", "future"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			st, e := store.Open(ctx, filepath.Join(t.TempDir(), "a.db"))
			if e != nil {
				t.Fatal(e)
			}
			defer st.Close()
			now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
			if e = st.EnsureAgent(ctx, "a"); e != nil {
				t.Fatal(e)
			}
			if mode != "no_baseline" {
				syntheticControls(t, st, "a", now)
			}
			if _, e = st.InitializePlaybook(ctx, "a", []string{"old"}, now); e != nil {
				t.Fatal(e)
			}
			entries, _ := st.PlaybookEntries(ctx, "a", 100)
			id := entries[0].ID
			actionID := reserveAction(t, st, "a", id, now)
			if mode == "revision" {
				if _, e = st.ProposePlaybook(ctx, "a", id, "new", now.Add(time.Minute)); e != nil {
					t.Fatal(e)
				}
			}
			at := now.Add(24 * time.Hour)
			if mode == "late" {
				at = now.Add(30*time.Hour + time.Nanosecond)
			}
			matureActions(t, st, "a", at, 90)
			if mode == "zero_views" {
				if _, e = st.DB().Exec(`UPDATE metrics SET views=0 WHERE action_id=?`, actionID); e != nil {
					t.Fatal(e)
				}
			}
			if mode == "kind" {
				if _, e = st.DB().Exec(`UPDATE actions SET kind='reply' WHERE id=?`, actionID); e != nil {
					t.Fatal(e)
				}
			}
			processAt := at
			if mode == "future" {
				processAt = at.Add(-time.Nanosecond)
			}
			if e = playbook.Process(ctx, st, "a", processAt, nil); e != nil {
				t.Fatal(e)
			}
			entries, e = st.PlaybookEntries(ctx, "a", 100)
			if e != nil || entries[0].N != 0 || entries[0].Successes != 0 {
				t.Fatalf("%+v %v", entries, e)
			}
			if mode == "revision" {
				var revision, index int64
				if e = st.DB().QueryRow(`SELECT revision,test_index FROM playbook_tests WHERE agent_id='a' ORDER BY test_index DESC LIMIT 1`).Scan(&revision, &index); e != nil || revision != 2 || index != 2 {
					t.Fatalf("revision=%d index=%d err=%v", revision, index, e)
				}
			}
		})
	}
}

func syntheticCurrentControls(t *testing.T, st *store.Store, agentID string, posted time.Time, count int, likes int64) []int64 {
	t.Helper()
	tx, e := st.DB().Begin()
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	ids := []int64{}
	for i := 0; i < count; i++ {
		at := posted.Add(time.Duration(i) * time.Second)
		measured := at.Add(24 * time.Hour)
		r, e := tx.Exec(`INSERT INTO actions(agent_id,kind,text,dry_run,posted_at,thread_url) VALUES(?,'post','PRIVATE CURRENT CONTROL',0,?,'https://fixture.invalid/current')`, agentID, at.Format(time.RFC3339Nano))
		if e != nil {
			t.Fatal(e)
		}
		id, e := r.LastInsertId()
		if e != nil {
			t.Fatal(e)
		}
		ids = append(ids, id)
		if _, e = tx.Exec(`INSERT INTO metrics(agent_id,action_id,measured_at,views,likes) VALUES(?,?,?,100,?)`, agentID, id, measured.Format(time.RFC3339Nano), likes); e != nil {
			t.Fatal(e)
		}
		for _, h := range []int{1, 6, 24} {
			due := at.Add(time.Duration(h) * time.Hour).Format("2006-01-02T15:04:05.000000000Z")
			if _, e = tx.Exec(`INSERT INTO metric_jobs(agent_id,action_id,window_hours,due_at,not_before,completed_at) VALUES(?,?,?,?,?,?)`, agentID, id, h, due, due, measured.Format(time.RFC3339Nano)); e != nil {
				t.Fatal(e)
			}
		}
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	return ids
}

func TestCommonShiftsUseExactlyDayMatchedControlIDs(t *testing.T) {
	for _, mode := range []string{"start_up", "mid_down", "mid_up", "missing_day", "fractional_day", "foreign_controls", "wrong_kind", "stale_controls", "future_controls"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			st, e := store.Open(ctx, filepath.Join(t.TempDir(), "aligned.db"))
			if e != nil {
				t.Fatal(e)
			}
			defer st.Close()
			base := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
			if e = st.EnsureAgent(ctx, "a"); e != nil {
				t.Fatal(e)
			}
			if e = st.EnsureAgent(ctx, "foreign"); e != nil {
				t.Fatal(e)
			}
			syntheticControls(t, st, "a", base)
			// Historically zero reference successes makes old-vs-current drift obvious.
			if _, e = st.DB().Exec(`UPDATE metrics SET likes=5 WHERE action_id IN (SELECT id FROM actions ORDER BY id LIMIT 200 OFFSET 100)`); e != nil {
				t.Fatal(e)
			}
			if _, e = st.InitializePlaybook(ctx, "a", []string{"idea"}, base); e != nil {
				t.Fatal(e)
			}
			entries, _ := st.PlaybookEntries(ctx, "a", 100)
			hyp := entries[0].ID
			split := 25
			if mode == "fractional_day" {
				split = 49
			}
			for i := 0; i < 50; i++ {
				at := base.Add(time.Duration(i) * time.Minute)
				if i >= split {
					at = at.Add(24 * time.Hour)
				}
				reserveAction(t, st, "a", hyp, at)
			}
			firstLikes, secondLikes := int64(90), int64(90)
			if mode == "mid_down" {
				secondLikes = 0
			}
			if mode == "mid_up" {
				firstLikes = 0
			}
			sourceAgent := "a"
			if mode == "foreign_controls" {
				sourceAgent = "foreign"
			}
			wanted := syntheticCurrentControls(t, st, sourceAgent, base.Add(10*time.Minute), 100, firstLikes)
			day2 := base.Add(24*time.Hour + 10*time.Minute)
			if mode == "missing_day" {
				day2 = day2.Add(24 * time.Hour)
			}
			wanted = append(wanted, syntheticCurrentControls(t, st, sourceAgent, day2, 100, secondLikes)...)
			if mode == "wrong_kind" {
				if _, e = st.DB().Exec(`UPDATE actions SET kind='reply' WHERE text='PRIVATE CURRENT CONTROL'`); e != nil {
					t.Fatal(e)
				}
			}
			if mode == "stale_controls" || mode == "future_controls" {
				stamp := base.Add(60 * time.Hour).Format(time.RFC3339Nano)
				if _, e = st.DB().Exec(`UPDATE metrics SET measured_at=? WHERE action_id IN (SELECT id FROM actions WHERE text='PRIVATE CURRENT CONTROL')`, stamp); e != nil {
					t.Fatal(e)
				}
				if _, e = st.DB().Exec(`UPDATE metric_jobs SET completed_at=? WHERE action_id IN (SELECT id FROM actions WHERE text='PRIVATE CURRENT CONTROL')`, stamp); e != nil {
					t.Fatal(e)
				}
			}
			matureActions(t, st, "a", base.Add(25*time.Hour), firstLikes)
			if e = playbook.Process(ctx, st, "a", base.Add(25*time.Hour), nil); e != nil {
				t.Fatal(e)
			}
			matureActions(t, st, "a", base.Add(49*time.Hour), secondLikes)
			processAt := base.Add(49 * time.Hour)
			if mode == "stale_controls" {
				processAt = base.Add(70 * time.Hour)
			}
			if e = playbook.Process(ctx, st, "a", processAt, nil); e != nil {
				t.Fatal(e)
			}
			entries, e = st.PlaybookEntries(ctx, "a", 100)
			if e != nil || entries[0].Status == "established" || entries[0].N != 50 {
				t.Fatalf("%+v %v", entries, e)
			}
			var raw, reason string
			var n int
			if e = st.DB().QueryRow(`SELECT control_ids_json,current_n,reason FROM playbook_looks WHERE agent_id='a' AND n=50`).Scan(&raw, &n, &reason); e != nil {
				t.Fatal(e)
			}
			supported := mode == "start_up" || mode == "mid_up" || mode == "mid_down"
			if supported {
				var got []int64
				if e = json.Unmarshal([]byte(raw), &got); e != nil || !reflect.DeepEqual(got, wanted) || n != 200 {
					t.Fatalf("control IDs=%s wanted=%v n=%d err=%v", raw, wanted, n, e)
				}
			} else if n != 0 || reason != "current_controls_unavailable" {
				t.Fatalf("n=%d reason=%s", n, reason)
			}
			before := raw
			// Extra controls arriving after a look was consumed never reopen that look.
			syntheticCurrentControls(t, st, "a", base.Add(20*time.Minute), 200, 0)
			if e = playbook.Process(ctx, st, "a", base.Add(72*time.Hour), nil); e != nil {
				t.Fatal(e)
			}
			if e = st.DB().QueryRow(`SELECT control_ids_json FROM playbook_looks WHERE agent_id='a' AND n=50`).Scan(&raw); e != nil || raw != before {
				t.Fatal("consumed look changed")
			}
		})
	}
}

func TestFractionalDayAllocationConsumesLookWithoutRounding(t *testing.T) {
	ctx := context.Background()
	st, e := store.Open(ctx, filepath.Join(t.TempDir(), "fractional.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	base := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	if e = st.EnsureAgent(ctx, "a"); e != nil {
		t.Fatal(e)
	}
	syntheticControls(t, st, "a", base)
	if _, e = st.InitializePlaybook(ctx, "a", []string{"idea"}, base); e != nil {
		t.Fatal(e)
	}
	entries, _ := st.PlaybookEntries(ctx, "a", 100)
	hyp := entries[0].ID
	first := reserveAction(t, st, "a", hyp, base)
	// Saved synthetic code-owned checkpoint immediately before the N=400 look.
	tx, e := st.DB().Begin()
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	for i := 0; i < 399; i++ {
		id := first
		if i > 0 {
			r, e := tx.Exec(`INSERT INTO actions(agent_id,kind,text,hypothesis_ids_json,dry_run,posted_at,thread_url) VALUES('a','post','checkpoint',?,0,?,'https://fixture.invalid/checkpoint')`, fmt.Sprintf("[%d]", hyp), base.Add(time.Duration(i)*time.Second).Format(time.RFC3339Nano))
			if e != nil {
				t.Fatal(e)
			}
			id, e = r.LastInsertId()
			if e != nil {
				t.Fatal(e)
			}
		}
		if _, e = tx.Exec(`INSERT INTO playbook_outcomes(agent_id,action_id,hypothesis_id,revision,measured_at,success,reason,counted) VALUES('a',?,?,1,?,1,'counted',1)`, id, hyp, base.Add(25*time.Hour).Format(time.RFC3339Nano)); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = tx.Exec(`UPDATE playbook SET n=399,successes=399 WHERE id=?`, hyp); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	last := reserveAction(t, st, "a", hyp, base.Add(24*time.Hour))
	syntheticCurrentControls(t, st, "a", base.Add(time.Hour), 250, 0)
	syntheticCurrentControls(t, st, "a", base.Add(25*time.Hour), 250, 0)
	due := base.Add(48 * time.Hour).Format("2006-01-02T15:04:05.000000000Z")
	if _, e = st.DB().Exec(`INSERT INTO metric_jobs(agent_id,action_id,window_hours,due_at,not_before) VALUES('a',?,24,?,?)`, last, due, due); e != nil {
		t.Fatal(e)
	}
	at := base.Add(50 * time.Hour)
	jobs, e := st.ClaimMetricJobs(ctx, "a", at, 5*time.Minute, 1)
	if e != nil || len(jobs) != 1 {
		t.Fatalf("jobs=%v err=%v", jobs, e)
	}
	if _, e = st.CompleteMetricJobs(ctx, jobs, at, store.MetricValues{Views: 100, Likes: 90}); e != nil {
		t.Fatal(e)
	}
	if e = playbook.Process(ctx, st, "a", at, nil); e != nil {
		t.Fatal(e)
	}
	var n, controls int
	var status, reason string
	if e = st.DB().QueryRow(`SELECT n,current_n,status,reason FROM playbook_looks WHERE agent_id='a'`).Scan(&n, &controls, &status, &reason); e != nil || n != 400 || controls != 0 || status != "testing" || reason != "current_controls_unavailable" {
		t.Fatalf("n=%d controls=%d %s %s err=%v", n, controls, status, reason, e)
	}
	if e = playbook.Process(ctx, st, "a", at.Add(time.Hour), nil); e != nil {
		t.Fatal(e)
	}
	var looks int
	if e = st.DB().QueryRow(`SELECT count(*) FROM playbook_looks WHERE n=400`).Scan(&looks); e != nil || looks != 1 {
		t.Fatal("look reopened")
	}
}

func TestBaselineFreezeExcludesInvalidOrFutureControls(t *testing.T) {
	for _, mode := range []string{"foreign", "kind", "dry", "unknown", "tagged", "future", "stale", "wrong_window", "zero_views"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			st, e := store.Open(ctx, filepath.Join(t.TempDir(), "baseline.db"))
			if e != nil {
				t.Fatal(e)
			}
			defer st.Close()
			base := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
			if e = st.EnsureAgent(ctx, "a"); e != nil {
				t.Fatal(e)
			}
			syntheticControls(t, st, "a", base)
			statement := ""
			var args []any
			switch mode {
			case "foreign":
				statement = `UPDATE actions SET agent_id='foreign' WHERE id=1`
			case "kind":
				statement = `UPDATE actions SET kind='reply',target_username='fixture_control',target_url='https://fixture.invalid/control' WHERE id=1`
			case "dry":
				statement = `UPDATE actions SET dry_run=1 WHERE id=1`
			case "unknown":
				statement = `UPDATE actions SET thread_url='' WHERE id=1`
			case "tagged":
				statement = `UPDATE actions SET hypothesis_ids_json='[999]' WHERE id=1`
			case "future", "stale":
				at := base
				if mode == "stale" {
					at = base.Add(-41 * time.Hour)
				}
				statement = `UPDATE metrics SET measured_at=? WHERE action_id=1`
				args = []any{at.Format(time.RFC3339Nano)}
			case "wrong_window":
				statement = `DELETE FROM metric_jobs WHERE action_id=1 AND window_hours=24`
			case "zero_views":
				statement = `UPDATE metrics SET views=0 WHERE action_id=1`
			}
			if _, e = st.DB().Exec(statement, args...); e != nil {
				t.Fatal(e)
			}
			if mode == "future" || mode == "stale" {
				if _, e = st.DB().Exec(`UPDATE metric_jobs SET completed_at=? WHERE action_id=1`, args[0]); e != nil {
					t.Fatal(e)
				}
			}
			if _, e = st.InitializePlaybook(ctx, "a", []string{"idea"}, base); e != nil {
				t.Fatal(e)
			}
			entries, _ := st.PlaybookEntries(ctx, "a", 100)
			id := reserveAction(t, st, "a", entries[0].ID, base)
			var baselines, eligible int
			if e = st.DB().QueryRow(`SELECT count(*) FROM playbook_baselines`).Scan(&baselines); e != nil || baselines != 0 {
				t.Fatalf("baselines=%d err=%v", baselines, e)
			}
			if e = st.DB().QueryRow(`SELECT eligible FROM action_hypotheses WHERE action_id=?`, id).Scan(&eligible); e != nil || eligible != 0 {
				t.Fatalf("eligible=%d err=%v", eligible, e)
			}
		})
	}
}

func TestGateLookUsesActualObservationOrder(t *testing.T) {
	ctx := context.Background()
	st, e := store.Open(ctx, filepath.Join(t.TempDir(), "order.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	base := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	if e = st.EnsureAgent(ctx, "a"); e != nil {
		t.Fatal(e)
	}
	syntheticControls(t, st, "a", base)
	if _, e = st.InitializePlaybook(ctx, "a", []string{"idea"}, base); e != nil {
		t.Fatal(e)
	}
	entries, _ := st.PlaybookEntries(ctx, "a", 100)
	hyp := entries[0].ID
	var first int64
	for i := 0; i < 50; i++ {
		id := reserveAction(t, st, "a", hyp, base.Add(time.Duration(i)*time.Minute))
		if i == 0 {
			first = id
		}
	}
	syntheticCurrentControls(t, st, "a", base.Add(10*time.Minute), 200, 10)
	if _, e = st.SeedMetricJobs(ctx, "a"); e != nil {
		t.Fatal(e)
	}
	at := base.Add(25 * time.Hour)
	jobs, e := st.ClaimMetricJobs(ctx, "a", at, time.Hour, 1000)
	if e != nil {
		t.Fatal(e)
	}
	groups := map[int64][]store.MetricJob{}
	for _, job := range jobs {
		groups[job.ActionID] = append(groups[job.ActionID], job)
	}
	late := at.Add(30 * time.Minute)
	for id, group := range groups {
		measured := at
		if id == first {
			measured = late
		}
		if _, e = st.CompleteMetricJobs(ctx, group, measured, store.MetricValues{Views: 100, Likes: 90}); e != nil {
			t.Fatal(e)
		}
	}
	if e = playbook.Process(ctx, st, "a", late, nil); e != nil {
		t.Fatal(e)
	}
	var stamp string
	if e = st.DB().QueryRow(`SELECT measured_at FROM playbook_looks WHERE n=50`).Scan(&stamp); e != nil {
		t.Fatal(e)
	}
	got, e := time.Parse(time.RFC3339Nano, stamp)
	if e != nil || !got.Equal(late) {
		t.Fatalf("look timestamp=%s wanted=%s err=%v", stamp, late, e)
	}
}
