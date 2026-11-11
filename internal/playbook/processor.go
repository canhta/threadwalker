package playbook

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/canhta/threadwalker/internal/logging"
	"github.com/canhta/threadwalker/internal/store"
)

const MaxMetricAge = 30 * time.Hour
const BaselineMaxAge = 30 * 24 * time.Hour

// Reserve snapshots each testing revision inside the guard's existing writer
// transaction. Only fixed harness code calls this; it is never a model tool.
func Reserve(ctx context.Context, conn *sql.Conn, agentID string, actionID int64, ids []int64, at time.Time) error {
	var kind string
	if err := conn.QueryRowContext(ctx, `SELECT kind FROM actions WHERE agent_id=? AND id=?`, agentID, actionID).Scan(&kind); err != nil {
		return fmt.Errorf("binding playbook action: %w", err)
	}
	for _, id := range ids {
		var revision int64
		if err := conn.QueryRowContext(ctx, `SELECT revision FROM playbook WHERE agent_id=? AND id=? AND status='testing'`, agentID, id).Scan(&revision); err != nil {
			return fmt.Errorf("binding testing revision: %w", err)
		}
		var baselineKind string
		err := conn.QueryRowContext(ctx, `SELECT kind FROM playbook_baselines WHERE agent_id=? AND hypothesis_id=? AND revision=?`, agentID, id, revision).Scan(&baselineKind)
		if errors.Is(err, sql.ErrNoRows) {
			controls, e := readControls(ctx, conn, agentID, kind, at.Add(-BaselineMaxAge), at)
			if e != nil {
				return e
			}
			if len(controls) >= BaselineWindowSize {
				controls = controls[len(controls)-BaselineWindowSize:]
				scores := make([]float64, len(controls))
				ids := make([]int64, len(controls))
				for i, c := range controls {
					scores[i] = c.score
					ids[i] = c.id
				}
				provenance, _ := json.Marshal(ids)
				b, e := BuildBaseline(scores)
				if e != nil {
					return fmt.Errorf("building playbook baseline: %w", e)
				}
				if _, e = conn.ExecContext(ctx, `INSERT INTO playbook_baselines(agent_id,hypothesis_id,revision,kind,threshold,n,successes,frozen_at,control_ids_json) VALUES(?,?,?,?,?,?,?,?,?)`, agentID, id, revision, kind, b.Threshold, b.N, b.Successes, at.UTC().Format(time.RFC3339Nano), string(provenance)); e != nil {
					return fmt.Errorf("freezing playbook baseline: %w", e)
				}
				baselineKind = kind
			}
		} else if err != nil {
			return fmt.Errorf("reading playbook baseline: %w", err)
		}
		if _, err = conn.ExecContext(ctx, `INSERT INTO action_hypotheses(agent_id,action_id,hypothesis_id,revision,eligible) VALUES(?,?,?,?,?)`, agentID, actionID, id, revision, baselineKind == kind); err != nil {
			return fmt.Errorf("snapshotting testing revision: %w", err)
		}
	}
	return nil
}

type control struct {
	id         int64
	at, posted time.Time
	score      float64
}

func readControls(ctx context.Context, conn *sql.Conn, agentID, kind string, from, to time.Time) ([]control, error) {
	rows, err := conn.QueryContext(ctx, `SELECT a.id,a.posted_at,m.measured_at,m.views,m.likes,m.replies,m.reposts FROM actions a JOIN metric_jobs j ON j.agent_id=a.agent_id AND j.action_id=a.id AND j.window_hours=24 JOIN metrics m ON m.agent_id=j.agent_id AND m.action_id=j.action_id AND m.measured_at=j.completed_at WHERE a.agent_id=? AND a.kind=? AND a.dry_run=0 AND trim(a.thread_url)<>'' AND trim(a.hypothesis_ids_json)='[]' AND julianday(a.posted_at)>=julianday(?) AND julianday(a.posted_at)<=julianday(?)`, agentID, kind, from.UTC().Format(time.RFC3339Nano), to.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, fmt.Errorf("reading baseline controls: %w", err)
	}
	defer rows.Close()
	controls := []control{}
	for rows.Next() {
		var id, v, l, r, p int64
		var posted, measured string
		if err = rows.Scan(&id, &posted, &measured, &v, &l, &r, &p); err != nil {
			return nil, fmt.Errorf("reading baseline control: %w", err)
		}
		start, e := time.Parse(time.RFC3339Nano, posted)
		if e != nil {
			return nil, errors.New("baseline control: invalid timestamp")
		}
		measuredAt, e := time.Parse(time.RFC3339Nano, measured)
		if e != nil {
			return nil, errors.New("baseline control: invalid timestamp")
		}
		if start.Before(from) || !start.Before(to) || !measuredAt.Before(to) || measuredAt.Before(start.Add(24*time.Hour)) || measuredAt.After(start.Add(MaxMetricAge)) {
			continue
		}
		score, e := EngagementScore(v, l, r, p)
		if e != nil {
			continue
		}
		controls = append(controls, control{id, measuredAt, start, score})
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("reading baseline controls: %w", err)
	}
	sort.Slice(controls, func(i, j int) bool {
		if controls[i].at.Equal(controls[j].at) {
			return controls[i].id < controls[j].id
		}
		return controls[i].at.Before(controls[j].at)
	})
	return controls, nil
}

// currentControls matches the complete candidate cohort's UTC posting-day
// histogram exactly. Integer allocations are never rounded. Old candidate days
// are never silently replaced by recent controls; absent support fails closed.
func currentControls(ctx context.Context, conn *sql.Conn, agentID string, o observation, n int64, threshold float64, frozenAt time.Time, observedAt time.Time) ([]int64, int64, error) {
	rows, err := conn.QueryContext(ctx, `SELECT a.posted_at FROM playbook_outcomes p JOIN actions a ON a.id=p.action_id AND a.agent_id=p.agent_id WHERE p.agent_id=? AND p.hypothesis_id=? AND p.revision=? AND p.counted=1`, agentID, o.id, o.revision)
	if err != nil {
		return nil, 0, fmt.Errorf("reading candidate days: %w", err)
	}
	days := map[string]int64{}
	count := int64(1)
	for rows.Next() {
		var stamp string
		if err = rows.Scan(&stamp); err != nil {
			rows.Close()
			return nil, 0, err
		}
		at, e := time.Parse(time.RFC3339Nano, stamp)
		if e != nil {
			rows.Close()
			return nil, 0, errors.New("candidate day: invalid timestamp")
		}
		days[at.UTC().Format("2006-01-02")]++
		count++
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, 0, err
	}
	if count != n {
		return nil, 0, errors.New("matching candidate days: counter mismatch")
	}
	posted, err := time.Parse(time.RFC3339Nano, o.posted)
	if err != nil {
		return nil, 0, err
	}
	days[posted.UTC().Format("2006-01-02")]++
	for _, count := range days {
		if count*BaselineReferenceSize%n != 0 {
			return nil, 0, nil
		}
	}
	controls, err := readControls(ctx, conn, agentID, o.kind, frozenAt, observedAt)
	if err != nil {
		return nil, 0, err
	}
	byDay := map[string][]control{}
	for _, c := range controls {
		day := c.posted.UTC().Format("2006-01-02")
		byDay[day] = append(byDay[day], c)
	}
	keys := make([]string, 0, len(days))
	for day := range days {
		keys = append(keys, day)
	}
	sort.Strings(keys)
	selected := []int64{}
	successes := int64(0)
	for _, day := range keys {
		need := days[day] * BaselineReferenceSize / n
		available := byDay[day]
		if int64(len(available)) < need {
			return nil, 0, nil
		}
		for _, c := range available[:int(need)] {
			success, err := OutcomeSuccess(c.score, threshold)
			if err != nil {
				return nil, 0, err
			}
			if success {
				successes++
			}
			selected = append(selected, c.id)
		}
	}
	return selected, successes, nil
}

func gateLook(n int64) bool {
	switch n {
	case 50, 100, 200, 400, 800, 1600:
		return true
	}
	return false
}

type observation struct {
	action, id, revision           int64
	eligible                       bool
	kind, posted, measured         string
	views, likes, replies, reposts int64
}
type decisionEvent struct {
	event                                                           string
	baselineN, baselineSuccesses, controlN, controlSuccesses, index int64
	id, revision, n, successes                                      int64
	status, reason                                                  string
}

// Process reconciles completed harness-owned 24h targets. Outcome markers,
// counters and decisions commit together, so retries/restarts cannot add twice.
// Failed transactions return no decisions and retain no partial counts.
func Process(ctx context.Context, st *store.Store, agentID string, now time.Time, logger *slog.Logger) error {
	if st == nil || agentID == "" || now.IsZero() {
		return errors.New("processing playbook: store, agent and time required")
	}
	conn, err := st.DB().Conn(ctx)
	if err != nil {
		return fmt.Errorf("opening playbook transaction: %w", err)
	}
	defer conn.Close()
	if _, err = conn.ExecContext(ctx, `PRAGMA busy_timeout=1000`); err != nil {
		return fmt.Errorf("configuring playbook transaction: %w", err)
	}
	if _, err = conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return fmt.Errorf("starting playbook transaction: %w", err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, _ = conn.ExecContext(cleanup, `ROLLBACK`)
	}()
	var status string
	if err = conn.QueryRowContext(ctx, `SELECT status FROM agents WHERE id=?`, agentID).Scan(&status); err != nil {
		return fmt.Errorf("reading playbook agent: %w", err)
	}
	events := []decisionEvent{}
	if status == "running" {
		items, e := observations(ctx, conn, agentID)
		if e != nil {
			return e
		}
		for _, o := range items {
			at, e := time.Parse(time.RFC3339Nano, o.measured)
			if e != nil {
				return errors.New("processing playbook: invalid measurement timestamp")
			}
			if at.After(now) {
				continue
			}
			start, e := time.Parse(time.RFC3339Nano, o.posted)
			if e != nil {
				return errors.New("processing playbook: invalid action timestamp")
			}
			var currentRevision, n, s, index int64
			var currentStatus string
			e = conn.QueryRowContext(ctx, `SELECT p.revision,p.status,p.n,p.successes,t.test_index FROM playbook p JOIN playbook_tests t ON t.agent_id=p.agent_id AND t.hypothesis_id=p.id AND t.revision=p.revision WHERE p.agent_id=? AND p.id=?`, agentID, o.id).Scan(&currentRevision, &currentStatus, &n, &s, &index)
			if e != nil {
				return fmt.Errorf("reading outcome hypothesis: %w", e)
			}
			reason := "counted"
			counted, success := false, false
			switch {
			case currentRevision != o.revision:
				reason = "revision_changed"
			case currentStatus != "testing":
				reason = "already_decided"
			case !o.eligible:
				reason = "baseline_unavailable"
			case at.Before(start.Add(24*time.Hour)) || at.After(start.Add(MaxMetricAge)):
				reason = "metric_age_excluded"
			default:
				var b Baseline
				var kind, frozen string
				e = conn.QueryRowContext(ctx, `SELECT kind,threshold,n,successes,frozen_at FROM playbook_baselines WHERE agent_id=? AND hypothesis_id=? AND revision=?`, agentID, o.id, o.revision).Scan(&kind, &b.Threshold, &b.N, &b.Successes, &frozen)
				if e != nil {
					return fmt.Errorf("reading frozen baseline: %w", e)
				}
				if kind != o.kind {
					reason = "kind_mismatch"
					break
				}
				score, e := EngagementScore(o.views, o.likes, o.replies, o.reposts)
				if e != nil {
					reason = "metrics_unavailable"
					break
				}
				success, e = OutcomeSuccess(score, b.Threshold)
				if e != nil {
					return e
				}
				counted = true
				n++
				if success {
					s++
				}
				var controlIDs []int64
				var controlSuccesses int64
				if gateLook(n) {
					frozenAt, e := time.Parse(time.RFC3339Nano, frozen)
					if e != nil {
						return errors.New("reading frozen baseline time: invalid timestamp")
					}
					controlIDs, controlSuccesses, e = currentControls(ctx, conn, agentID, o, n, b.Threshold, frozenAt, at)
					if e != nil {
						return e
					}
				}
				decision, e := EvaluateGate(GateInput{N: n, Successes: s, BaselineN: b.N, BaselineSuccesses: b.Successes, TestIndex: index, ControlN: int64(len(controlIDs)), ControlSuccesses: controlSuccesses})
				if e != nil {
					return fmt.Errorf("evaluating playbook gate: %w", e)
				}
				if gateLook(n) {
					raw, _ := json.Marshal(controlIDs)
					if _, e = conn.ExecContext(ctx, `INSERT INTO playbook_looks(agent_id,hypothesis_id,revision,n,current_n,current_successes,control_ids_json,measured_at,status,reason) VALUES(?,?,?,?,?,?,?,?,?,?)`, agentID, o.id, o.revision, n, len(controlIDs), controlSuccesses, string(raw), o.measured, decision.Status, decision.Reason); e != nil {
						return fmt.Errorf("consuming playbook look: %w", e)
					}
				}
				if gateLook(n) {
					events = append(events, decisionEvent{event: logging.PlaybookGateChecked, id: o.id, revision: o.revision, n: n, successes: s, status: decision.Status, reason: decision.Reason, baselineN: b.N, baselineSuccesses: b.Successes, controlN: int64(len(controlIDs)), controlSuccesses: controlSuccesses, index: index})
				}
				var decided any
				if decision.Status != "testing" {
					decided = now.UTC().Format(time.RFC3339Nano)
				}
				if _, e = conn.ExecContext(ctx, `UPDATE playbook SET n=?,successes=?,status=?,decided_at=? WHERE agent_id=? AND id=? AND revision=? AND status='testing'`, n, s, decision.Status, decided, agentID, o.id, o.revision); e != nil {
					return fmt.Errorf("counting playbook outcome: %w", e)
				}
				if decision.Status != "testing" {
					if _, e = conn.ExecContext(ctx, `INSERT INTO playbook_decisions(agent_id,hypothesis_id,revision,status,n,successes,reason,decided_at) VALUES(?,?,?,?,?,?,?,?)`, agentID, o.id, o.revision, decision.Status, n, s, decision.Reason, decided); e != nil {
						return fmt.Errorf("recording playbook decision: %w", e)
					}
					events = append(events, decisionEvent{event: logging.PlaybookDecided, id: o.id, revision: o.revision, n: n, successes: s, status: decision.Status, reason: decision.Reason, baselineN: b.N, baselineSuccesses: b.Successes, controlN: int64(len(controlIDs)), controlSuccesses: controlSuccesses, index: index})
				}
			}
			if _, e = conn.ExecContext(ctx, `INSERT INTO playbook_outcomes(agent_id,action_id,hypothesis_id,revision,measured_at,success,reason,counted) VALUES(?,?,?,?,?,?,?,?)`, agentID, o.action, o.id, o.revision, o.measured, success, reason, counted); e != nil {
				return fmt.Errorf("recording playbook outcome: %w", e)
			}
		}
	}
	if _, err = conn.ExecContext(ctx, `COMMIT`); err != nil {
		return fmt.Errorf("committing playbook transaction: %w", err)
	}
	for _, e := range events {
		logging.Emit(ctx, logger, slog.LevelInfo, logging.Record{Event: e.event, AgentID: agentID, Operation: "playbook_gate", Outcome: e.status, Reason: e.reason, HypothesisID: e.id, Revision: e.revision, N: e.n, Successes: e.successes, BaselineN: e.baselineN, BaselineSuccesses: e.baselineSuccesses, ControlN: e.controlN, ControlSuccesses: e.controlSuccesses, TestIndex: e.index})
	}
	return nil
}

func observations(ctx context.Context, conn *sql.Conn, agentID string) ([]observation, error) {
	rows, err := conn.QueryContext(ctx, `SELECT h.action_id,h.hypothesis_id,h.revision,h.eligible,a.kind,a.posted_at,m.measured_at,m.views,m.likes,m.replies,m.reposts FROM action_hypotheses h JOIN actions a ON a.agent_id=h.agent_id AND a.id=h.action_id JOIN metric_jobs j ON j.agent_id=a.agent_id AND j.action_id=a.id AND j.window_hours=24 JOIN metrics m ON m.agent_id=j.agent_id AND m.action_id=j.action_id AND m.measured_at=j.completed_at WHERE h.agent_id=? AND a.dry_run=0 AND trim(a.thread_url)<>'' AND NOT EXISTS(SELECT 1 FROM playbook_outcomes o WHERE o.agent_id=h.agent_id AND o.action_id=h.action_id AND o.hypothesis_id=h.hypothesis_id) ORDER BY j.due_at,h.action_id,h.hypothesis_id LIMIT 1000`, agentID)
	if err != nil {
		return nil, fmt.Errorf("reading playbook observations: %w", err)
	}
	defer rows.Close()
	out := []observation{}
	for rows.Next() {
		var o observation
		if err = rows.Scan(&o.action, &o.id, &o.revision, &o.eligible, &o.kind, &o.posted, &o.measured, &o.views, &o.likes, &o.replies, &o.reposts); err != nil {
			return nil, fmt.Errorf("reading playbook observation: %w", err)
		}
		out = append(out, o)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("reading playbook observations: %w", err)
	}
	return out, nil
}
