package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Bounds on the rows one snapshot returns. Publications and the draft count are
// not bounded: a total must never stop at a page of rows.
const (
	snapshotWakes      = 20
	snapshotToolCalls  = 1000
	snapshotTrendRows  = 2000
	snapshotSamples    = 4000
	snapshotLedgerDays = 100
	snapshotPhaseRows  = 300
)

// DashboardQuery scopes one snapshot to the range from From (the first day at
// 00:00 UTC) to Now.
type DashboardQuery struct {
	AgentID string
	From    time.Time
	Now     time.Time
}

// DashboardSnapshot is every stored fact the dashboard shows for one agent,
// read in one transaction. Timestamps are zero when the stored value is not
// RFC 3339. Text columns are cut to a bounded length.
type DashboardSnapshot struct {
	Started      bool
	Status       string
	NextWake     time.Time
	Drafts       int
	Publications []DashboardPublication
	LastWake     *DashboardWake
	Wakes        []DashboardWake
	ToolCalls    []DashboardToolCall
	Trends       []DashboardTrend
	Ledger       []DashboardLedgerDay
	Phases       []DashboardPhaseSpend
	Metrics      []DashboardMetric
	// SpendTotal is every dollar the agent's ledger holds, whatever the range.
	SpendTotal float64
	// Lessons are the active lessons and the latest retired ones, with their use.
	Lessons    []Lesson
	LessonUses map[int64]LessonUse
}

// snapshotRetiredLessons bounds the retired lessons one snapshot lists.
const snapshotRetiredLessons = 20

// DashboardPublication is a real (not dry-run) action, newest first. URL is
// empty when the stored link is longer than 512 bytes. Text is the post text, cut to a bounded
// length.
type DashboardPublication struct {
	ID              int64
	Kind            string
	PostedAt        time.Time
	URL             string
	FollowersAtPost *int64
	// Deleted is set when a metric read found the post gone from Threads.
	Deleted bool
	Text    string
	// Band is the episode band at the newest window; empty when the action has
	// no measurement. Rank, Pool and Window are zero when it is unranked.
	Band         string
	Rank         float64
	Pool, Window int
}

type DashboardWake struct {
	ID        int64
	StartedAt time.Time
	EndedAt   time.Time
	Ended     bool
	Kind      string
	Steps     int
	CostUSD   float64
	EndReason string
}

// DashboardToolCall carries Result only when it is at most 1024 bytes.
type DashboardToolCall struct {
	ID        int64
	WakeID    int64
	Tool      string
	Result    string
	BlockedBy string
}

type DashboardTrend struct {
	Title      string
	Source     string
	Rank       int
	ObservedAt time.Time
}

type DashboardLedgerDay struct {
	Day     string
	CostUSD float64
}

type DashboardPhaseSpend struct {
	Day                 string
	Phase               string
	CostUSD             float64
	TokensIn, TokensOut int64
}

// DashboardMetric is one cumulative measurement of a real post or reply, grouped
// by action newest first and oldest measurement first within an action.
type DashboardMetric struct {
	ActionID                               int64
	MeasuredAt                             time.Time
	Views, Likes, Replies, Reposts, Quotes int64
	// Followers is the account's follower count at the measurement, nil when unread.
	Followers *int64
}

// DashboardSnapshot reads one agent's dashboard facts in a single read-only
// transaction. An agent without a row is reported as not Started.
func (s *Store) DashboardSnapshot(ctx context.Context, q DashboardQuery) (DashboardSnapshot, error) {
	var snap DashboardSnapshot
	if q.AgentID == "" || q.From.IsZero() || q.Now.Before(q.From) {
		return snap, errors.New("reading dashboard snapshot: invalid scope")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return snap, fmt.Errorf("reading dashboard snapshot: %w", err)
	}
	defer tx.Rollback()
	r := snapshotReader{ctx: ctx, tx: tx, q: q}
	for _, step := range []func(*DashboardSnapshot) error{
		r.agent, r.actions, r.wakes, r.toolCalls, r.trends, r.spend, r.metrics,
	} {
		if err = step(&snap); err != nil {
			return DashboardSnapshot{}, fmt.Errorf("reading dashboard snapshot: %w", err)
		}
	}
	// The episode and lesson reads use their own connection, so the snapshot's
	// transaction ends first.
	if err = tx.Rollback(); err != nil {
		return DashboardSnapshot{}, fmt.Errorf("reading dashboard snapshot: %w", err)
	}
	if err = s.episodesAndLessons(ctx, q.AgentID, &snap); err != nil {
		return DashboardSnapshot{}, fmt.Errorf("reading dashboard snapshot: %w", err)
	}
	return snap, nil
}

// episodesAndLessons adds each publication's band and the agent's lessons.
func (s *Store) episodesAndLessons(ctx context.Context, agentID string, snap *DashboardSnapshot) error {
	eps, err := s.Episodes(ctx, agentID, EpisodeQuery{})
	if err != nil {
		return err
	}
	byID := make(map[int64]Episode, len(eps))
	for _, e := range eps {
		byID[e.ActionID] = e
	}
	for i, p := range snap.Publications {
		if e, ok := byID[p.ID]; ok {
			snap.Publications[i].Band, snap.Publications[i].Rank, snap.Publications[i].Pool, snap.Publications[i].Window = e.Band, e.Rank, e.Pool, e.Window
		}
	}
	all, err := s.Lessons(ctx, agentID, true)
	if err != nil {
		return err
	}
	retired := 0
	for _, l := range all {
		if !l.Active() {
			if retired++; retired > snapshotRetiredLessons {
				continue
			}
		}
		snap.Lessons = append(snap.Lessons, l)
	}
	snap.LessonUses, err = s.LessonUses(ctx, agentID, snap.Lessons)
	return err
}

type snapshotReader struct {
	ctx context.Context
	tx  *sql.Tx
	q   DashboardQuery
}

func (r snapshotReader) rows(query string, args ...any) (*sql.Rows, error) {
	return r.tx.QueryContext(r.ctx, query, args...)
}

// window is the half of every time-range predicate: rows between from and the
// query's Now, compared as instants whatever offset or precision was stored.
func (r snapshotReader) window(col string) string {
	return "julianday(" + col + ")>=julianday(?) AND julianday(" + col + ")<=julianday(?)"
}

func (r snapshotReader) span(from time.Time) []any {
	return []any{from.Format(time.RFC3339Nano), r.q.Now.Format(time.RFC3339Nano)}
}

func stamp(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil || t.Year() < 1 || t.Year() > 9999 {
		return time.Time{}
	}
	return t.UTC()
}

func (r snapshotReader) agent(snap *DashboardSnapshot) error {
	var next string
	err := r.tx.QueryRowContext(r.ctx, `SELECT status,COALESCE(next_wake_at,'') FROM agents WHERE id=?`, r.q.AgentID).Scan(&snap.Status, &next)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	snap.Started = true
	snap.NextWake = stamp(next)
	return nil
}

func (r snapshotReader) actions(snap *DashboardSnapshot) error {
	if err := r.tx.QueryRowContext(r.ctx, `SELECT COUNT(*) FROM actions WHERE agent_id=? AND dry_run=1`, r.q.AgentID).Scan(&snap.Drafts); err != nil {
		return err
	}
	rows, err := r.rows(`SELECT a.id,substr(a.kind,1,16),a.posted_at,CASE WHEN length(a.thread_url)<=512 THEN a.thread_url ELSE '' END,a.followers_at_post,EXISTS(SELECT 1 FROM metric_jobs j WHERE j.agent_id=a.agent_id AND j.action_id=a.id AND j.reason='media_gone'),substr(a.text,1,600) FROM actions a WHERE a.agent_id=? AND a.dry_run=0 ORDER BY a.id DESC`, r.q.AgentID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var p DashboardPublication
		var at string
		if err = rows.Scan(&p.ID, &p.Kind, &at, &p.URL, &p.FollowersAtPost, &p.Deleted, &p.Text); err != nil {
			return err
		}
		p.PostedAt = stamp(at)
		snap.Publications = append(snap.Publications, p)
	}
	return rows.Err()
}

func (r snapshotReader) wakes(snap *DashboardSnapshot) error {
	const cols = `id,substr(started_at,1,64),substr(ended_at,1,64),COALESCE(substr(kind,1,32),''),steps,cost_usd,COALESCE(substr(end_reason,1,64),'')`
	scan := func(rows *sql.Rows) (DashboardWake, error) {
		var w DashboardWake
		var started string
		var ended sql.NullString
		if err := rows.Scan(&w.ID, &started, &ended, &w.Kind, &w.Steps, &w.CostUSD, &w.EndReason); err != nil {
			return w, err
		}
		w.StartedAt = stamp(started)
		w.Ended = ended.Valid
		w.EndedAt = stamp(ended.String)
		return w, nil
	}
	rows, err := r.rows(`SELECT `+cols+` FROM wakes WHERE agent_id=? AND `+r.window("started_at")+` ORDER BY julianday(started_at) DESC,id DESC LIMIT ?`, append(append([]any{r.q.AgentID}, r.span(r.q.From)...), snapshotWakes)...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		w, err := scan(rows)
		if err != nil {
			return err
		}
		snap.Wakes = append(snap.Wakes, w)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	last, err := r.rows(`SELECT `+cols+` FROM wakes WHERE agent_id=? ORDER BY julianday(started_at) DESC,id DESC LIMIT 1`, r.q.AgentID)
	if err != nil {
		return err
	}
	defer last.Close()
	if last.Next() {
		w, err := scan(last)
		if err != nil {
			return err
		}
		snap.LastWake = &w
	}
	return last.Err()
}

func (r snapshotReader) toolCalls(snap *DashboardSnapshot) error {
	rows, err := r.rows(`SELECT id,wake_id,substr(tool,1,64),CASE WHEN length(result_json)<=1024 THEN result_json ELSE '' END,substr(blocked_by,1,64) FROM tool_calls WHERE agent_id=? AND `+r.window("at")+` ORDER BY id DESC LIMIT ?`, append(append([]any{r.q.AgentID}, r.span(r.q.From)...), snapshotToolCalls)...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var t DashboardToolCall
		if err = rows.Scan(&t.ID, &t.WakeID, &t.Tool, &t.Result, &t.BlockedBy); err != nil {
			return err
		}
		snap.ToolCalls = append(snap.ToolCalls, t)
	}
	return rows.Err()
}

func (r snapshotReader) trends(snap *DashboardSnapshot) error {
	rows, err := r.rows(`SELECT substr(title,1,300),substr(source,1,120),rank,observed_at FROM trend_stories WHERE agent_id=? AND `+r.window("observed_at")+` ORDER BY julianday(observed_at) DESC,id DESC LIMIT ?`, append(append([]any{r.q.AgentID}, r.span(r.q.From)...), snapshotTrendRows)...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var t DashboardTrend
		var at string
		if err = rows.Scan(&t.Title, &t.Source, &t.Rank, &at); err != nil {
			return err
		}
		t.ObservedAt = stamp(at)
		snap.Trends = append(snap.Trends, t)
	}
	return rows.Err()
}

func (r snapshotReader) spend(snap *DashboardSnapshot) error {
	from := r.q.From.Format("2006-01-02")
	rows, err := r.rows(`SELECT day,SUM(cost_usd) FROM ledger_phases WHERE agent_id=? AND day>=? GROUP BY day ORDER BY day LIMIT ?`, r.q.AgentID, from, snapshotLedgerDays)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var d DashboardLedgerDay
		if err = rows.Scan(&d.Day, &d.CostUSD); err != nil {
			return err
		}
		snap.Ledger = append(snap.Ledger, d)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if err = r.tx.QueryRowContext(r.ctx, `SELECT COALESCE(SUM(cost_usd),0) FROM ledger_phases WHERE agent_id=?`, r.q.AgentID).Scan(&snap.SpendTotal); err != nil {
		return err
	}
	phases, err := r.rows(`SELECT day,phase,cost_usd,tokens_in,tokens_out FROM ledger_phases WHERE agent_id=? AND day>=? ORDER BY day,phase LIMIT ?`, r.q.AgentID, from, snapshotPhaseRows)
	if err != nil {
		return err
	}
	defer phases.Close()
	for phases.Next() {
		var p DashboardPhaseSpend
		if err = phases.Scan(&p.Day, &p.Phase, &p.CostUSD, &p.TokensIn, &p.TokensOut); err != nil {
			return err
		}
		snap.Phases = append(snap.Phases, p)
	}
	return phases.Err()
}

func (r snapshotReader) metrics(snap *DashboardSnapshot) error {
	rows, err := r.rows(`SELECT m.action_id,m.measured_at,m.views,m.likes,m.replies,m.reposts,COALESCE(m.quotes,0),m.followers FROM metrics m JOIN actions a ON a.id=m.action_id AND a.agent_id=m.agent_id WHERE m.agent_id=? AND a.dry_run=0 AND a.kind IN ('post','reply') ORDER BY a.id DESC,julianday(m.measured_at) LIMIT ?`, r.q.AgentID, snapshotSamples)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var m DashboardMetric
		var at string
		if err = rows.Scan(&m.ActionID, &at, &m.Views, &m.Likes, &m.Replies, &m.Reposts, &m.Quotes, &m.Followers); err != nil {
			return err
		}
		m.MeasuredAt = stamp(at)
		snap.Metrics = append(snap.Metrics, m)
	}
	return rows.Err()
}
