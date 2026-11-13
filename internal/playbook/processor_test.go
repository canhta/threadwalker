package playbook_test

import (
	"bytes"
	"context"
	"fmt"
	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/guard"
	"github.com/canhta/threadz/internal/logging"
	"math/rand"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/playbook"
	"github.com/canhta/threadz/internal/store"
)

const minEffect = 0.2

func TestMetricOutcomesRequireFrozenRevisionAndCompleted24HourTarget(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "playbook.db"))
	if err != nil {
		t.Fatal(err)
	}
	outcomeTriggers(t, st)
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
	if err = playbook.Process(ctx, st, "a", minEffect, now.Add(24*time.Hour), nil); err != nil {
		t.Fatal(err)
	}
	entries, err := st.PlaybookEntries(ctx, "a", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].N != 0 {
		t.Fatalf("entries=%+v", entries)
	}
}

// outcomeTriggers stand in for the metrics processor, which stores a code-owned
// outcome per measured action: more than 10 likes in 100 views is a lift above
// the expected value, anything else below it, and zero views has no outcome.
func outcomeTriggers(t *testing.T, st *store.Store) {
	t.Helper()
	for _, stmt := range []string{
		`CREATE TRIGGER outcome_ai AFTER INSERT ON metrics WHEN new.views>0 BEGIN INSERT OR REPLACE INTO action_outcomes(agent_id,action_id,measured_at,model_version,views,conversation_rate,applause_rate,reach_lift,resonance_lift,label) VALUES(new.agent_id,new.action_id,new.measured_at,1,new.views,0,0,CASE WHEN new.likes>10 THEN 1 ELSE -1 END,CASE WHEN new.likes>10 THEN 1 ELSE -1 END,'ordinary'); END`,
		`CREATE TRIGGER outcome_au AFTER UPDATE ON metrics BEGIN DELETE FROM action_outcomes WHERE agent_id=new.agent_id AND action_id=new.action_id AND new.views<=0; UPDATE action_outcomes SET reach_lift=CASE WHEN new.likes>10 THEN 1 ELSE -1 END,resonance_lift=CASE WHEN new.likes>10 THEN 1 ELSE -1 END WHERE agent_id=new.agent_id AND action_id=new.action_id; END`,
	} {
		if _, err := st.DB().Exec(stmt); err != nil {
			t.Fatal(err)
		}
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
	g, err := guard.New(guard.Scope{Store: st, Template: tmpl, WakeID: wakeID, AllowLive: true, Now: func() time.Time { return at }})
	if err != nil {
		t.Fatal(err)
	}
	var id int64
	_, err = g.Execute(ctx, guard.Attempt{Tool: "post", Arguments: fmt.Sprintf(`{"text":"candidate%d","hypothesis_ids":[%d]}`, at.UnixNano(), hypothesisID), ReservedActionID: &id}, func() (string, error) {
		ok, e := st.ConfirmPublished(ctx, agentID, id, "https://fixture.invalid/candidate", "candidate")
		if e == nil && !ok {
			e = fmt.Errorf("action %d not confirmable", id)
		}
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
	if _, e := st.SeedMetricJobs(ctx, agentID, time.Unix(0, 0)); e != nil {
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

func openOutcomes(t *testing.T, path string, hypotheses ...string) (*store.Store, int64, time.Time) {
	t.Helper()
	ctx := context.Background()
	st, e := store.Open(ctx, path)
	if e != nil {
		t.Fatal(e)
	}
	outcomeTriggers(t, st)
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	for _, agent := range []string{"a", "other"} {
		if e = st.EnsureAgent(ctx, agent); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = st.InitializePlaybook(ctx, "a", append([]string{"PRIVATE HYPOTHESIS"}, hypotheses...), now); e != nil {
		t.Fatal(e)
	}
	if _, e = st.InitializePlaybook(ctx, "other", []string{"foreign"}, now); e != nil {
		t.Fatal(e)
	}
	entries, e := st.PlaybookEntries(ctx, "a", 100)
	if e != nil {
		t.Fatal(e)
	}
	return st, entries[0].ID, now
}

func TestOutcomesCountOnceAndEstablishWithMetadataOnlyDecision(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "outcomes.db")
	st, id, now := openOutcomes(t, path)
	defer func() { st.Close() }()
	const tagged = playbook.MinActions + 2
	for i := 0; i < tagged; i++ {
		reserveAction(t, st, "a", id, now.Add(time.Duration(i)*time.Minute))
	}
	// Immature targets do not count. In particular 1h and 6h results are not evidence.
	matureActions(t, st, "a", now.Add(7*time.Hour), 90)
	if e := playbook.Process(ctx, st, "a", minEffect, now.Add(7*time.Hour), nil); e != nil {
		t.Fatal(e)
	}
	entries, e := st.PlaybookEntries(ctx, "a", 100)
	if e != nil || entries[0].N != 0 {
		t.Fatalf("early %+v %v", entries, e)
	}
	matureActions(t, st, "a", now.Add(25*time.Hour), 90)
	// A DB failure must roll back counts and outcome markers together.
	if _, e = st.DB().Exec(`CREATE TRIGGER fail_outcome BEFORE INSERT ON playbook_outcomes BEGIN SELECT RAISE(ABORT,'PRIVATE FAILURE'); END`); e != nil {
		t.Fatal(e)
	}
	if e = playbook.Process(ctx, st, "a", minEffect, now.Add(25*time.Hour), nil); e == nil {
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
	if e = playbook.Process(ctx, st, "a", minEffect, now.Add(25*time.Hour), logger); e != nil {
		t.Fatal(e)
	}
	// The decision lands at the minimum number of actions; later ones are recorded and not counted.
	entries, e = st.PlaybookEntries(ctx, "a", 100)
	if e != nil || entries[0].Status != "established" || entries[0].N != playbook.MinActions {
		t.Fatalf("entries=%+v err=%v", entries, e)
	}
	var markers, counted int
	if e = st.DB().QueryRow(`SELECT COUNT(*),SUM(counted) FROM playbook_outcomes`).Scan(&markers, &counted); e != nil || markers != tagged || counted != playbook.MinActions {
		t.Fatalf("markers=%d counted=%d err=%v", markers, counted, e)
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
	if e = playbook.Process(ctx, st, "a", minEffect, now.Add(26*time.Hour), logger); e != nil {
		t.Fatal(e)
	}
	entries, e = st.PlaybookEntries(ctx, "a", 100)
	if e != nil || entries[0].N != playbook.MinActions || logs.String() != before {
		t.Fatal("restart duplicated evidence or decision")
	}
}

func TestNegativeLiftsRejectAndTheSuggestionFollowsTheTestingPosteriors(t *testing.T) {
	ctx := context.Background()
	st, bad, now := openOutcomes(t, filepath.Join(t.TempDir(), "reject.db"), "second")
	defer st.Close()
	entries, _ := st.PlaybookEntries(ctx, "a", 100)
	good := entries[1].ID
	for i := 0; i < playbook.MinActions; i++ {
		reserveAction(t, st, "a", bad, now.Add(time.Duration(i)*time.Minute))
	}
	matureActions(t, st, "a", now.Add(25*time.Hour), 5)
	if e := playbook.Process(ctx, st, "a", minEffect, now.Add(25*time.Hour), nil); e != nil {
		t.Fatal(e)
	}
	entries, _ = st.PlaybookEntries(ctx, "a", 100)
	if entries[0].ID != good || entries[0].Status != "testing" || entries[1].ID != bad || entries[1].Status != "rejected" {
		t.Fatalf("entries=%+v", entries)
	}
	var reason string
	if e := st.DB().QueryRow(`SELECT reason FROM playbook_decisions WHERE hypothesis_id=?`, bad).Scan(&reason); e != nil || reason != "negative_evidence" {
		t.Fatalf("reason=%q err=%v", reason, e)
	}
	// Only the testing hypothesis can be suggested, whatever the source draws.
	for seed := int64(0); seed < 20; seed++ {
		s, e := playbook.Suggest(ctx, st, "a", rand.New(rand.NewSource(seed)))
		if e != nil || s == nil || s.HypothesisID != good || s.Reason == "" {
			t.Fatalf("suggestion=%+v err=%v", s, e)
		}
	}
	if s, e := playbook.Suggest(ctx, st, "other", rand.New(rand.NewSource(1))); e != nil || s == nil || s.HypothesisID == good || s.HypothesisID == bad {
		t.Fatalf("foreign suggestion=%+v err=%v", s, e)
	}
}

func TestAmendmentAndStaleMetricsCannotInheritEvidence(t *testing.T) {
	for _, mode := range []string{"revision", "late", "zero_views", "future"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			st, id, now := openOutcomes(t, filepath.Join(t.TempDir(), "a.db"))
			defer st.Close()
			actionID := reserveAction(t, st, "a", id, now)
			if mode == "revision" {
				if _, e := st.ProposePlaybook(ctx, "a", id, "new", now.Add(time.Minute), ""); e != nil {
					t.Fatal(e)
				}
			}
			at := now.Add(24 * time.Hour)
			if mode == "late" {
				at = now.Add(30*time.Hour + time.Nanosecond)
			}
			matureActions(t, st, "a", at, 90)
			if mode == "zero_views" {
				if _, e := st.DB().Exec(`UPDATE metrics SET views=0 WHERE action_id=?`, actionID); e != nil {
					t.Fatal(e)
				}
			}
			processAt := at
			if mode == "future" {
				processAt = at.Add(-time.Nanosecond)
			}
			if e := playbook.Process(ctx, st, "a", minEffect, processAt, nil); e != nil {
				t.Fatal(e)
			}
			entries, e := st.PlaybookEntries(ctx, "a", 100)
			if e != nil || entries[0].N != 0 {
				t.Fatalf("%+v %v", entries, e)
			}
		})
	}
}

func TestAnActionOfAnotherKindThanTheFirstCountedOneAddsNothing(t *testing.T) {
	ctx := context.Background()
	st, id, now := openOutcomes(t, filepath.Join(t.TempDir(), "kind.db"))
	defer st.Close()
	reserveAction(t, st, "a", id, now)
	second := reserveAction(t, st, "a", id, now.Add(time.Minute))
	matureActions(t, st, "a", now.Add(25*time.Hour), 90)
	if _, e := st.DB().Exec(`UPDATE actions SET kind='reply' WHERE id=?`, second); e != nil {
		t.Fatal(e)
	}
	if e := playbook.Process(ctx, st, "a", minEffect, now.Add(25*time.Hour), nil); e != nil {
		t.Fatal(e)
	}
	entries, _ := st.PlaybookEntries(ctx, "a", 100)
	var reason string
	if entries[0].N != 1 {
		t.Fatalf("entries=%+v", entries)
	}
	if e := st.DB().QueryRow(`SELECT reason FROM playbook_outcomes WHERE action_id=?`, second).Scan(&reason); e != nil || reason != "kind_mismatch" {
		t.Fatalf("reason=%q err=%v", reason, e)
	}
}
