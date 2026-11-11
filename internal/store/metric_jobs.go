package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// MetricValues contains counters measured together for one published action.
type MetricValues struct{ Views, Likes, Replies, Reposts int64 }

// MetricJob identifies an immutable measurement target and its current fence.
type MetricJob struct {
	AgentID      string
	ActionID     int64
	WindowHours  int
	DueAt        time.Time
	PostedAt     time.Time
	ThreadURL    string
	ClaimVersion int64
}

// ErrMetricClaim identifies an invalid, stale, foreign or expired metric claim.
var ErrMetricClaim = errors.New("metric claim unavailable")

// Fixed-width UTC timestamps preserve nanosecond ordering in SQLite TEXT.
const metricTimeLayout = "2006-01-02T15:04:05.000000000Z"

func metricTime(at time.Time) string { return at.UTC().Format(metricTimeLayout) }
func validMetricTime(at time.Time) bool {
	return !at.IsZero() && at.UTC().Year() >= 1 && at.UTC().Year() <= 9999
}

// metricWrite reserves the SQLite writer before reading, across connections and
// processes. Nothing survives a failed or cancelled transaction.
func (s *Store) metricWrite(ctx context.Context, fn func(*sql.Conn) error) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("opening metric transaction: %w", err)
	}
	defer conn.Close()
	if _, err = conn.ExecContext(ctx, `PRAGMA busy_timeout=1000`); err != nil {
		return fmt.Errorf("configuring metric transaction: %w", err)
	}
	if _, err = conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return fmt.Errorf("starting metric transaction: %w", err)
	}
	defer func() {
		rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, _ = conn.ExecContext(rollbackCtx, `ROLLBACK`)
	}()
	if err = fn(conn); err != nil {
		return err
	}
	if _, err = conn.ExecContext(ctx, `COMMIT`); err != nil {
		return fmt.Errorf("committing metric transaction: %w", err)
	}
	return nil
}

// SeedMetricJobs creates fixed targets only from confirmed non-dry-run actions.
// Invalid publish times fail the entire seed operation without guessing a clock.
func (s *Store) SeedMetricJobs(ctx context.Context, agentID string) (int, error) {
	if agentID == "" {
		return 0, errors.New("seeding metrics: missing agent")
	}
	count := 0
	err := s.metricWrite(ctx, func(conn *sql.Conn) error {
		rows, err := conn.QueryContext(ctx, `SELECT a.id,a.posted_at FROM actions a JOIN agents g ON g.id=a.agent_id WHERE a.agent_id=? AND a.dry_run=0 AND trim(a.thread_url)<>'' ORDER BY a.id`, agentID)
		if err != nil {
			return fmt.Errorf("reading metric actions: %w", err)
		}
		type action struct {
			id int64
			at time.Time
		}
		actions := []action{}
		for rows.Next() {
			var a action
			var stamp string
			if err = rows.Scan(&a.id, &stamp); err != nil {
				rows.Close()
				return fmt.Errorf("reading metric action: %w", err)
			}
			a.at, err = time.Parse(time.RFC3339Nano, stamp)
			if err != nil || !validMetricTime(a.at) || !validMetricTime(a.at.Add(24*time.Hour)) {
				rows.Close()
				return errors.New("seeding metrics: invalid published timestamp")
			}
			actions = append(actions, a)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return fmt.Errorf("reading metric actions: %w", err)
		}
		for _, a := range actions {
			for _, hours := range []int{1, 6, 24} {
				due := metricTime(a.at.Add(time.Duration(hours) * time.Hour))
				r, err := conn.ExecContext(ctx, `INSERT INTO metric_jobs(agent_id,action_id,window_hours,due_at,not_before) VALUES(?,?,?,?,?) ON CONFLICT(agent_id,action_id,window_hours) DO NOTHING`, agentID, a.id, hours, due, due)
				if err != nil {
					return fmt.Errorf("seeding metric target: %w", err)
				}
				n, err := r.RowsAffected()
				if err != nil {
					return fmt.Errorf("counting metric targets: %w", err)
				}
				count += int(n)
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return count, nil
}

// ClaimMetricJobs leases up to limit action groups, coalescing their ready
// targets. A live lease on any target excludes the entire action group.
func (s *Store) ClaimMetricJobs(ctx context.Context, agentID string, now time.Time, lease time.Duration, limit int) ([]MetricJob, error) {
	if agentID == "" || !validMetricTime(now) || lease <= 0 || !validMetricTime(now.Add(lease)) || limit < 1 || limit > 1000 {
		return nil, errors.New("claiming metrics: invalid options")
	}
	jobs := []MetricJob{}
	err := s.metricWrite(ctx, func(conn *sql.Conn) error {
		stamp := metricTime(now)
		rows, err := conn.QueryContext(ctx, `SELECT j.action_id FROM metric_jobs j JOIN actions a ON a.id=j.action_id AND a.agent_id=j.agent_id JOIN agents g ON g.id=j.agent_id WHERE j.agent_id=? AND j.completed_at IS NULL AND g.status='running' AND a.dry_run=0 AND trim(a.thread_url)<>'' GROUP BY j.action_id HAVING MIN(MAX(j.due_at,j.not_before))<=? AND (MAX(j.lease_until) IS NULL OR MAX(j.lease_until)<=?) ORDER BY MIN(MAX(j.due_at,j.not_before)),j.action_id LIMIT ?`, agentID, stamp, stamp, limit)
		if err != nil {
			return fmt.Errorf("selecting metric claims: %w", err)
		}
		ids := []int64{}
		for rows.Next() {
			var id int64
			if err = rows.Scan(&id); err != nil {
				rows.Close()
				return fmt.Errorf("reading metric claim group: %w", err)
			}
			ids = append(ids, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return fmt.Errorf("reading metric claim groups: %w", err)
		}
		for _, id := range ids {
			var version int64
			if err = conn.QueryRowContext(ctx, `SELECT MAX(claim_version)+1 FROM metric_jobs WHERE agent_id=? AND action_id=?`, agentID, id).Scan(&version); err != nil {
				return fmt.Errorf("reading metric fence: %w", err)
			}
			if _, err = conn.ExecContext(ctx, `UPDATE metric_jobs SET claim_version=?,claimed_at=?,lease_until=?,reason='' WHERE agent_id=? AND action_id=? AND completed_at IS NULL AND due_at<=? AND not_before<=?`, version, stamp, metricTime(now.Add(lease)), agentID, id, stamp, stamp); err != nil {
				return fmt.Errorf("leasing metric targets: %w", err)
			}
			rows, err = conn.QueryContext(ctx, `SELECT j.window_hours,j.due_at,a.posted_at,a.thread_url FROM metric_jobs j JOIN actions a ON a.id=j.action_id AND a.agent_id=j.agent_id WHERE j.agent_id=? AND j.action_id=? AND j.claim_version=? AND j.completed_at IS NULL ORDER BY j.window_hours`, agentID, id, version)
			if err != nil {
				return fmt.Errorf("reading leased metrics: %w", err)
			}
			for rows.Next() {
				job := MetricJob{AgentID: agentID, ActionID: id, ClaimVersion: version}
				var due, posted string
				if err = rows.Scan(&job.WindowHours, &due, &posted, &job.ThreadURL); err != nil {
					rows.Close()
					return fmt.Errorf("reading leased metric: %w", err)
				}
				job.DueAt, err = time.Parse(time.RFC3339Nano, due)
				if err != nil {
					rows.Close()
					return errors.New("claiming metrics: invalid target timestamp")
				}
				job.PostedAt, err = time.Parse(time.RFC3339Nano, posted)
				if err != nil || !validMetricTime(job.PostedAt) || !job.DueAt.Equal(job.PostedAt.Add(time.Duration(job.WindowHours)*time.Hour)) {
					rows.Close()
					return errors.New("claiming metrics: invalid published timestamp")
				}
				jobs = append(jobs, job)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return fmt.Errorf("reading leased metrics: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return jobs, nil
}

// NextMetricDue includes retry not-before times and action-wide lease fences.
func (s *Store) NextMetricDue(ctx context.Context, agentID string) (time.Time, bool, error) {
	var stamp sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT MIN(CASE WHEN last_lease IS NOT NULL AND earliest<last_lease THEN last_lease ELSE earliest END) FROM (SELECT MIN(COALESCE(j.lease_until,MAX(j.due_at,j.not_before))) AS earliest,MAX(j.lease_until) AS last_lease FROM metric_jobs j JOIN agents g ON g.id=j.agent_id JOIN actions a ON a.id=j.action_id AND a.agent_id=j.agent_id WHERE j.agent_id=? AND g.status='running' AND a.dry_run=0 AND trim(a.thread_url)<>'' AND j.completed_at IS NULL GROUP BY j.action_id)`, agentID).Scan(&stamp)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("reading next metric target: %w", err)
	}
	if !stamp.Valid {
		return time.Time{}, false, nil
	}
	at, err := time.Parse(time.RFC3339Nano, stamp.String)
	if err != nil || !validMetricTime(at) {
		return time.Time{}, false, errors.New("reading next metric target: invalid timestamp")
	}
	return at, true, nil
}

// metricClaims checks the full claim set and immutable action binding while the
// writer lock is held. It returns true only for the same already-completed set.
func metricClaims(ctx context.Context, conn *sql.Conn, jobs []MetricJob, at time.Time, requireRunning bool) (bool, error) {
	if len(jobs) < 1 || len(jobs) > 3 || !validMetricTime(at) {
		return false, ErrMetricClaim
	}
	first := jobs[0]
	if first.AgentID == "" || first.ActionID <= 0 || first.ClaimVersion <= 0 {
		return false, ErrMetricClaim
	}
	wanted := make(map[int]MetricJob, len(jobs))
	for _, job := range jobs {
		if job.AgentID != first.AgentID || job.ActionID != first.ActionID || job.ClaimVersion != first.ClaimVersion || (job.WindowHours != 1 && job.WindowHours != 6 && job.WindowHours != 24) {
			return false, ErrMetricClaim
		}
		if _, ok := wanted[job.WindowHours]; ok {
			return false, ErrMetricClaim
		}
		wanted[job.WindowHours] = job
	}
	var posted, url, status string
	var dry int
	err := conn.QueryRowContext(ctx, `SELECT a.posted_at,a.thread_url,a.dry_run,g.status FROM actions a JOIN agents g ON g.id=a.agent_id WHERE a.id=? AND a.agent_id=?`, first.ActionID, first.AgentID).Scan(&posted, &url, &dry, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrMetricClaim
	}
	if err != nil {
		return false, fmt.Errorf("reading metric claim action: %w", err)
	}
	postedAt, err := time.Parse(time.RFC3339Nano, posted)
	if err != nil || !validMetricTime(postedAt) || dry != 0 || url == "" {
		return false, ErrMetricClaim
	}
	rows, err := conn.QueryContext(ctx, `SELECT window_hours,due_at,claim_version,claimed_at,lease_until,completed_at FROM metric_jobs WHERE agent_id=? AND action_id=?`, first.AgentID, first.ActionID)
	if err != nil {
		return false, fmt.Errorf("reading metric fences: %w", err)
	}
	defer rows.Close()
	found, completed, leased := 0, 0, 0
	for rows.Next() {
		var hours int
		var due string
		var version int64
		var start, lease, done sql.NullString
		if err = rows.Scan(&hours, &due, &version, &start, &lease, &done); err != nil {
			return false, fmt.Errorf("reading metric fence: %w", err)
		}
		if !done.Valid && lease.Valid {
			leased++
		}
		job, ok := wanted[hours]
		if !ok {
			continue
		}
		found++
		if version != job.ClaimVersion || !job.PostedAt.Equal(postedAt) || job.ThreadURL != url || !job.DueAt.Equal(postedAt.Add(time.Duration(hours)*time.Hour)) || metricTime(job.DueAt) != due {
			return false, ErrMetricClaim
		}
		if done.Valid {
			completed++
			continue
		}
		if !start.Valid || !lease.Valid || metricTime(at) < start.String || metricTime(at) >= lease.String || metricTime(at) < due {
			return false, ErrMetricClaim
		}
	}
	if err = rows.Err(); err != nil {
		return false, fmt.Errorf("reading metric fences: %w", err)
	}
	if found != len(jobs) {
		return false, ErrMetricClaim
	}
	if completed == len(jobs) {
		return true, nil
	}
	if completed > 0 || leased != len(jobs) || (requireRunning && status != "running") {
		return false, ErrMetricClaim
	}
	return false, nil
}

// CompleteMetricJobs commits one current sample and all claimed checkpoints in
// one transaction. An identical completed claim is an idempotent no-op.
func (s *Store) CompleteMetricJobs(ctx context.Context, jobs []MetricJob, at time.Time, values MetricValues) (bool, error) {
	for _, value := range []int64{values.Views, values.Likes, values.Replies, values.Reposts} {
		if value < 0 || value > 1<<53 {
			return false, errors.New("completing metrics: invalid counters")
		}
	}
	committed := false
	err := s.metricWrite(ctx, func(conn *sql.Conn) error {
		done, err := metricClaims(ctx, conn, jobs, at, true)
		if err != nil {
			return err
		}
		if done {
			return nil
		}
		first := jobs[0]
		measured := formatStoreTimestamp(at)
		if _, err = conn.ExecContext(ctx, `INSERT INTO metrics(agent_id,action_id,measured_at,views,likes,replies,reposts) VALUES(?,?,?,?,?,?,?)`, first.AgentID, first.ActionID, measured, values.Views, values.Likes, values.Replies, values.Reposts); err != nil {
			return fmt.Errorf("inserting metric sample: %w", err)
		}
		for _, job := range jobs {
			r, err := conn.ExecContext(ctx, `UPDATE metric_jobs SET completed_at=?,claimed_at=NULL,lease_until=NULL,reason='' WHERE agent_id=? AND action_id=? AND window_hours=? AND claim_version=? AND completed_at IS NULL`, measured, job.AgentID, job.ActionID, job.WindowHours, job.ClaimVersion)
			if err != nil {
				return fmt.Errorf("completing metric checkpoint: %w", err)
			}
			n, err := r.RowsAffected()
			if err != nil {
				return fmt.Errorf("checking metric checkpoint: %w", err)
			}
			if n != 1 {
				return ErrMetricClaim
			}
		}
		committed = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return committed, nil
}

// DeferMetricJobs releases only the current complete claim set. Reason codes
// cannot retain arbitrary backend errors, URLs, usernames or external content.
func (s *Store) DeferMetricJobs(ctx context.Context, jobs []MetricJob, now, next time.Time, reason string) error {
	switch reason {
	case "metrics_unavailable", "source_unavailable", "read_failed", "binding_mismatch", "auth_required", "cancelled", "persistence_failed", "measurement_failed":
	default:
		return errors.New("deferring metrics: invalid reason code")
	}
	if !validMetricTime(now) || !validMetricTime(next) || !next.After(now) {
		return errors.New("deferring metrics: future retry required")
	}
	return s.metricWrite(ctx, func(conn *sql.Conn) error {
		done, err := metricClaims(ctx, conn, jobs, now, false)
		if err != nil {
			return err
		}
		if done {
			return ErrMetricClaim
		}
		for _, job := range jobs {
			r, err := conn.ExecContext(ctx, `UPDATE metric_jobs SET not_before=?,claimed_at=NULL,lease_until=NULL,reason=? WHERE agent_id=? AND action_id=? AND window_hours=? AND claim_version=? AND completed_at IS NULL`, metricTime(next), reason, job.AgentID, job.ActionID, job.WindowHours, job.ClaimVersion)
			if err != nil {
				return fmt.Errorf("deferring metric target: %w", err)
			}
			n, err := r.RowsAffected()
			if err != nil {
				return fmt.Errorf("checking metric deferral: %w", err)
			}
			if n != 1 {
				return ErrMetricClaim
			}
		}
		return nil
	})
}

// BeginMetricWake opens an audit scope without touching the agent's schedule or
// self-note. Agent and maintenance wakes cannot overlap for the same agent.
func (s *Store) BeginMetricWake(ctx context.Context, agentID string, at time.Time) (int64, error) {
	if agentID == "" || !validMetricTime(at) {
		return 0, errors.New("starting metric wake: invalid agent or time")
	}
	var id int64
	err := s.metricWrite(ctx, func(conn *sql.Conn) error {
		r, err := conn.ExecContext(ctx, `INSERT INTO wakes(agent_id,started_at,kind) SELECT ?,?,'metrics' WHERE EXISTS(SELECT 1 FROM agents WHERE id=? AND status='running') AND NOT EXISTS(SELECT 1 FROM wakes WHERE agent_id=? AND ended_at IS NULL)`, agentID, formatStoreTimestamp(at), agentID, agentID)
		if err != nil {
			return fmt.Errorf("starting metric wake: %w", err)
		}
		n, err := r.RowsAffected()
		if err != nil {
			return fmt.Errorf("checking metric wake: %w", err)
		}
		if n != 1 {
			return errors.New("starting metric wake: agent unavailable")
		}
		id, err = r.LastInsertId()
		if err != nil {
			return fmt.Errorf("identifying metric wake: %w", err)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return id, nil
}
