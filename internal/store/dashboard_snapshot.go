package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Bounds on the rows one snapshot returns. Publications and the draft count are
// not bounded: a total must never stop at a page of rows.
const (
	snapshotWakes      = 20
	snapshotToolCalls  = 1000
	snapshotTrendRows  = 2000
	snapshotHypotheses = 20
	snapshotNotes      = 40
	snapshotSamples    = 4000
	snapshotLedgerDays = 100
	snapshotPhaseRows  = 300
	snapshotStatuses   = 10
)

// DashboardQuery scopes one snapshot to the range from From (the first day at
// 00:00 UTC) to Now. Content adds the agent's own text and decision evidence.
type DashboardQuery struct {
	AgentID string
	From    time.Time
	Now     time.Time
	Content bool
}

// DashboardSnapshot is every stored fact the dashboard shows for one agent,
// read in one transaction. Timestamps are zero when the stored value is not
// RFC 3339. Text columns are cut to a bounded length.
type DashboardSnapshot struct {
	Started       bool
	Status        string
	NextWake      time.Time
	MemoryIndex   []string
	MemoryJournal []JournalEntry
	Drafts        int
	Publications  []DashboardPublication
	LastWake      *DashboardWake
	Wakes         []DashboardWake
	ToolCalls     []DashboardToolCall
	Trends        []DashboardTrend
	Ledger        []DashboardLedgerDay
	Phases        []DashboardPhaseSpend
	Playbook      DashboardPlaybook
	Hypotheses    []DashboardHypothesis
	NoteCount     int
	Notes         []DashboardNote
	Metrics       []DashboardMetric
	Candidates    []KnowledgeCandidate
	// SpendTotal is every dollar the agent's ledger holds, whatever the range.
	SpendTotal float64
	// Lifts are the counted lifts behind every hypothesis, for its posterior.
	Lifts []PlaybookLift
	// Strategy holds the newest revisions of the agent's strategy, newest first.
	Strategy []DashboardStrategy
}

// DashboardStrategy is one stored strategy version.
type DashboardStrategy struct {
	Version   int64
	Text      string
	Citations []string
	CreatedAt time.Time
}

// DashboardPublication is a real (not dry-run) action, newest first. URL is
// empty when the stored link is longer than 512 bytes. Label is empty until the
// outcome model has scored the action. Text and Why (the texts of the
// hypotheses it applied) are set only when the query asks for content.
type DashboardPublication struct {
	ID                       int64
	Kind                     string
	PostedAt                 time.Time
	URL                      string
	Label                    string
	ReachLift, ResonanceLift float64
	FollowersAtPost          *int64
	// Deleted is set when a metric read found the post gone from Threads.
	Deleted bool
	Text    string
	Why     []string
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

type DashboardPlaybook struct {
	Testing, Established, Rejected int
	Samples                        int64
}

// DashboardHypothesis is newest first. Text, Decision and Outcomes are
// set only when the query asks for content.
type DashboardHypothesis struct {
	ID        int64
	Status    string
	N         int
	DecidedAt time.Time
	Text      string
	Decision  *DashboardDecision
	Outcomes  int64
}

type DashboardDecision struct {
	Status string
	Reason string
	At     time.Time
}

type DashboardNote struct {
	Text      string
	CreatedAt time.Time
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
		r.agent, r.actions, r.lifts, r.wakes, r.toolCalls, r.trends, r.spend, r.playbook, r.hypotheses, r.memoryIndex, r.strategy, r.notes, r.metrics, r.candidates,
	} {
		if err = step(&snap); err != nil {
			return DashboardSnapshot{}, fmt.Errorf("reading dashboard snapshot: %w", err)
		}
	}
	return snap, nil
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
	rows, err := r.rows(`SELECT a.id,substr(a.kind,1,16),a.posted_at,CASE WHEN length(a.thread_url)<=512 THEN a.thread_url ELSE '' END,COALESCE(o.label,''),COALESCE(o.reach_lift,0),COALESCE(o.resonance_lift,0),a.followers_at_post,EXISTS(SELECT 1 FROM metric_jobs j WHERE j.agent_id=a.agent_id AND j.action_id=a.id AND j.reason='media_gone'),substr(a.text,1,600) FROM actions a LEFT JOIN action_outcomes o ON o.agent_id=a.agent_id AND o.action_id=a.id WHERE a.agent_id=? AND a.dry_run=0 ORDER BY a.id DESC`, r.q.AgentID)
	if err != nil {
		return err
	}
	defer rows.Close()
	index := map[int64]int{}
	for rows.Next() {
		var p DashboardPublication
		var at string
		if err = rows.Scan(&p.ID, &p.Kind, &at, &p.URL, &p.Label, &p.ReachLift, &p.ResonanceLift, &p.FollowersAtPost, &p.Deleted, &p.Text); err != nil {
			return err
		}
		p.PostedAt = stamp(at)
		index[p.ID] = len(snap.Publications)
		snap.Publications = append(snap.Publications, p)
	}
	if err = rows.Err(); err != nil || !r.q.Content {
		return err
	}
	why, err := r.rows(`SELECT ah.action_id,substr(p.text,1,300) FROM action_hypotheses ah JOIN playbook p ON p.agent_id=ah.agent_id AND p.id=ah.hypothesis_id WHERE ah.agent_id=? ORDER BY ah.action_id,ah.hypothesis_id`, r.q.AgentID)
	if err != nil {
		return err
	}
	defer why.Close()
	for why.Next() {
		var id int64
		var text string
		if err = why.Scan(&id, &text); err != nil {
			return err
		}
		if i, ok := index[id]; ok {
			snap.Publications[i].Why = append(snap.Publications[i].Why, text)
		}
	}
	return why.Err()
}

func (r snapshotReader) lifts(snap *DashboardSnapshot) (err error) {
	snap.Lifts, err = playbookLifts(r.ctx, r.tx, r.q.AgentID)
	return err
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

func (r snapshotReader) playbook(snap *DashboardSnapshot) error {
	rows, err := r.rows(`SELECT status,COUNT(*),COALESCE(SUM(n),0) FROM playbook WHERE agent_id=? GROUP BY status LIMIT ?`, r.q.AgentID, snapshotStatuses)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var status string
		var n int
		var samples int64
		if err = rows.Scan(&status, &n, &samples); err != nil {
			return err
		}
		switch status {
		case "testing":
			snap.Playbook.Testing = n
		case "established":
			snap.Playbook.Established = n
		case "rejected":
			snap.Playbook.Rejected = n
		}
		snap.Playbook.Samples += samples
	}
	return rows.Err()
}

func (r snapshotReader) hypotheses(snap *DashboardSnapshot) error {
	rows, err := r.rows(`SELECT id,status,n,COALESCE(decided_at,''),substr(text,1,500) FROM playbook WHERE agent_id=? ORDER BY id DESC LIMIT ?`, r.q.AgentID, snapshotHypotheses)
	if err != nil {
		return err
	}
	defer rows.Close()
	index := map[int64]int{}
	var ids []any
	for rows.Next() {
		var h DashboardHypothesis
		var decided string
		if err = rows.Scan(&h.ID, &h.Status, &h.N, &decided, &h.Text); err != nil {
			return err
		}
		h.DecidedAt = stamp(decided)
		index[h.ID] = len(snap.Hypotheses)
		ids = append(ids, h.ID)
		snap.Hypotheses = append(snap.Hypotheses, h)
	}
	if err = rows.Err(); err != nil || !r.q.Content || len(ids) == 0 {
		return err
	}
	marks := strings.TrimRight(strings.Repeat("?,", len(ids)), ",")
	args := append([]any{r.q.AgentID}, ids...)

	decisions, err := r.rows(`SELECT hypothesis_id,status,substr(reason,1,300),COALESCE(decided_at,'') FROM playbook_decisions d WHERE agent_id=? AND hypothesis_id IN (`+marks+`) AND revision=(SELECT MAX(revision) FROM playbook_decisions WHERE agent_id=d.agent_id AND hypothesis_id=d.hypothesis_id)`, args...)
	if err != nil {
		return err
	}
	defer decisions.Close()
	for decisions.Next() {
		var id int64
		var d DashboardDecision
		var at string
		if err = decisions.Scan(&id, &d.Status, &d.Reason, &at); err != nil {
			return err
		}
		d.At = stamp(at)
		snap.Hypotheses[index[id]].Decision = &d
	}
	if err = decisions.Err(); err != nil {
		return err
	}
	for _, c := range []struct {
		table string
		set   func(*DashboardHypothesis, int64)
	}{
		{`playbook_outcomes WHERE counted=1 AND`, func(h *DashboardHypothesis, n int64) { h.Outcomes = n }},
	} {
		counts, err := r.rows(`SELECT hypothesis_id,COUNT(*) FROM `+c.table+` agent_id=? AND hypothesis_id IN (`+marks+`) GROUP BY hypothesis_id`, args...)
		if err != nil {
			return err
		}
		for counts.Next() {
			var id, n int64
			if err = counts.Scan(&id, &n); err != nil {
				counts.Close()
				return err
			}
			c.set(&snap.Hypotheses[index[id]], n)
		}
		err = counts.Err()
		counts.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// DashboardJournalShown is how many memory journal rows the snapshot shows.
const DashboardJournalShown = 30

func (r snapshotReader) memoryIndex(snap *DashboardSnapshot) error {
	if !r.q.Content {
		return nil
	}
	rows, err := r.rows(`SELECT text FROM memory_index WHERE agent_id=? AND archived_at IS NULL ORDER BY id`, r.q.AgentID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var text string
		if err = rows.Scan(&text); err != nil {
			return err
		}
		snap.MemoryIndex = append(snap.MemoryIndex, text)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	journalRows, err := r.rows(`SELECT id, entry_id, change, old_text, new_text, wake_id, phase, created_at FROM memory_journal WHERE agent_id=? ORDER BY id DESC LIMIT ?`, r.q.AgentID, DashboardJournalShown)
	if err != nil {
		return err
	}
	defer journalRows.Close()
	for journalRows.Next() {
		var j JournalEntry
		if err = journalRows.Scan(&j.ID, &j.EntryID, &j.Change, &j.OldText, &j.NewText, &j.WakeID, &j.Phase, &j.At); err != nil {
			return err
		}
		snap.MemoryJournal = append(snap.MemoryJournal, j)
	}
	return journalRows.Err()
}

// DashboardStrategyShown is how many strategy revisions the snapshot shows; one older revision is
// read to anchor the oldest diff.
const DashboardStrategyShown = 10

func (r snapshotReader) strategy(snap *DashboardSnapshot) error {
	if !r.q.Content {
		return nil
	}
	rows, err := r.rows(`SELECT version,substr(text,1,2000),citations_json,created_at FROM strategy_revisions WHERE agent_id=? ORDER BY version DESC LIMIT ?`, r.q.AgentID, DashboardStrategyShown+1)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var s DashboardStrategy
		var citations, at string
		if err = rows.Scan(&s.Version, &s.Text, &citations, &at); err != nil {
			return err
		}
		if json.Unmarshal([]byte(citations), &s.Citations) != nil {
			s.Citations = nil
		}
		s.CreatedAt = stamp(at)
		snap.Strategy = append(snap.Strategy, s)
	}
	return rows.Err()
}

func (r snapshotReader) notes(snap *DashboardSnapshot) error {
	if err := r.tx.QueryRowContext(r.ctx, `SELECT COUNT(*) FROM notes WHERE agent_id=?`, r.q.AgentID).Scan(&snap.NoteCount); err != nil || !r.q.Content {
		return err
	}
	rows, err := r.rows(`SELECT substr(text,1,500),created_at FROM notes WHERE agent_id=? ORDER BY id DESC LIMIT ?`, r.q.AgentID, snapshotNotes)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var n DashboardNote
		var at string
		if err = rows.Scan(&n.Text, &at); err != nil {
			return err
		}
		n.CreatedAt = stamp(at)
		snap.Notes = append(snap.Notes, n)
	}
	return rows.Err()
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

// candidates lists the newest candidate techniques; their text is agent-written
// content, so it is read only when the query asks for content.
func (r snapshotReader) candidates(snap *DashboardSnapshot) error {
	if !r.q.Content {
		return nil
	}
	rows, err := r.rows(`SELECT id,substr(technique,1,500),substr(source_url,1,512),created_at FROM knowledge_candidates WHERE agent_id=? ORDER BY id DESC LIMIT ?`, r.q.AgentID, MaxKnowledgeCandidates)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var c KnowledgeCandidate
		var at string
		if err = rows.Scan(&c.ID, &c.Technique, &c.SourceURL, &at); err != nil {
			return err
		}
		c.CreatedAt = stamp(at)
		snap.Candidates = append(snap.Candidates, c)
	}
	return rows.Err()
}
