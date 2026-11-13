package playbook

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"time"

	"github.com/canhta/threadz/internal/logging"
	"github.com/canhta/threadz/internal/store"
)

const MaxMetricAge = 30 * time.Hour

// Outcome reasons are persisted in playbook_outcomes.
const (
	reasonCounted         = "counted"
	reasonRevisionChanged = "revision_changed"
	reasonAlreadyDecided  = "already_decided"
	reasonMetricAge       = "metric_age_excluded"
	reasonKindMismatch    = "kind_mismatch"
)

// Reserve snapshots each testing revision inside the guard's existing writer
// transaction. Only fixed harness code calls this; it is never a model tool.
func Reserve(ctx context.Context, tx store.PlaybookTx, agentID string, actionID int64, ids []int64) error {
	for _, id := range ids {
		revision, err := tx.TestingRevision(ctx, agentID, id)
		if err != nil {
			return err
		}
		if err = tx.SnapshotRevision(ctx, agentID, actionID, id, revision); err != nil {
			return err
		}
	}
	return nil
}

type hypothesis struct {
	id     int64
	status string
	Evidence
}

// posteriors returns every hypothesis of the agent, in id order, with its
// posterior, from the lifts the outcome model stored. The combined lift of an
// action is the mean of its reach and resonance lifts.
func posteriors(lifts []store.PlaybookLift) ([]hypothesis, []Posterior) {
	var hs []hypothesis
	var combined [][]float64
	for _, l := range lifts {
		if len(hs) == 0 || hs[len(hs)-1].id != l.Hypothesis {
			hs = append(hs, hypothesis{id: l.Hypothesis, status: l.Status, Evidence: Evidence{Kind: l.Kind}})
			combined = append(combined, nil)
		}
		if l.Counted {
			combined[len(hs)-1] = append(combined[len(hs)-1], (l.ReachLift+l.ResonanceLift)/2)
		}
	}
	all := make([]Evidence, len(hs))
	for i := range hs {
		hs[i].Evidence = NewEvidence(hs[i].Kind, combined[i])
		all[i] = hs[i].Evidence
	}
	return hs, Posteriors(all)
}

// Standing is a hypothesis with the posterior of its mean lift.
type Standing struct {
	ID     int64
	Status string
	N      int64
	Posterior
}

// Standings returns every hypothesis in the lifts with its posterior, in id order.
func Standings(lifts []store.PlaybookLift) []Standing {
	hs, post := posteriors(lifts)
	out := make([]Standing, len(hs))
	for i, h := range hs {
		out[i] = Standing{ID: h.id, Status: h.status, N: h.N, Posterior: post[i]}
	}
	return out
}

type decisionEvent struct {
	id, revision, n int64
	status, reason  string
}

// Process reconciles completed harness-owned 24h targets. Outcome markers,
// counters and decisions commit together, so retries/restarts cannot add twice.
// Failed transactions return no decisions and retain no partial counts.
// minEffect is the template's smallest lift worth acting on.
func Process(ctx context.Context, st *store.Store, agentID string, minEffect float64, now time.Time, logger *slog.Logger) error {
	if st == nil || agentID == "" || now.IsZero() {
		return errors.New("processing playbook: store, agent and time required")
	}
	events := []decisionEvent{}
	err := st.PlaybookOutcomes(ctx, func(tx store.PlaybookTx) error {
		status, e := tx.AgentStatus(ctx, agentID)
		if e != nil || status != "running" {
			return e
		}
		items, e := tx.Observations(ctx, agentID)
		if e != nil {
			return e
		}
		for _, o := range items {
			ev, e := processObservation(ctx, tx, agentID, o, minEffect, now)
			if e != nil {
				return e
			}
			events = append(events, ev...)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, e := range events {
		logging.Emit(ctx, logger, slog.LevelInfo, logging.Record{Event: logging.PlaybookDecided, AgentID: agentID, Operation: "playbook_decision", Outcome: e.status, Reason: e.reason, HypothesisID: e.id, Revision: e.revision, N: e.n})
	}
	return nil
}

func processObservation(ctx context.Context, tx store.PlaybookTx, agentID string, o store.PlaybookObservation, minEffect float64, now time.Time) ([]decisionEvent, error) {
	at, e := time.Parse(time.RFC3339Nano, o.Measured)
	if e != nil {
		return nil, errors.New("processing playbook: invalid measurement timestamp")
	}
	if at.After(now) {
		return nil, nil
	}
	start, e := time.Parse(time.RFC3339Nano, o.Posted)
	if e != nil {
		return nil, errors.New("processing playbook: invalid action timestamp")
	}
	cur, e := tx.Counters(ctx, agentID, o.Hypothesis)
	if e != nil {
		return nil, e
	}
	reason := reasonCounted
	switch {
	case cur.Revision != o.Revision:
		reason = reasonRevisionChanged
	case cur.Status != "testing":
		reason = reasonAlreadyDecided
	case at.Before(start.Add(24*time.Hour)) || at.After(start.Add(MaxMetricAge)):
		reason = reasonMetricAge
	case cur.Kind != "" && cur.Kind != o.Kind:
		reason = reasonKindMismatch
	}
	counted := reason == reasonCounted
	if e = tx.RecordOutcome(ctx, agentID, store.PlaybookOutcome{Action: o.Action, Hypothesis: o.Hypothesis, Revision: o.Revision, MeasuredAt: o.Measured, Reason: reason, Counted: counted}); e != nil || !counted {
		return nil, e
	}
	lifts, e := tx.Lifts(ctx, agentID)
	if e != nil {
		return nil, e
	}
	hs, post := posteriors(lifts)
	i := 0
	for hs[i].id != o.Hypothesis {
		i++
	}
	n := hs[i].N
	decision := Decide(post[i], n, minEffect)
	var decidedAt *time.Time
	if decision.Status != "testing" {
		decidedAt = &now
	}
	if e = tx.Count(ctx, agentID, o.Hypothesis, o.Revision, n, o.Kind, decision.Status, decidedAt); e != nil {
		return nil, e
	}
	if decidedAt == nil {
		return nil, nil
	}
	if e = tx.RecordDecision(ctx, agentID, store.PlaybookDecision{Hypothesis: o.Hypothesis, Revision: o.Revision, N: n, Status: decision.Status, Reason: decision.Reason, DecidedAt: now}); e != nil {
		return nil, e
	}
	return []decisionEvent{{id: o.Hypothesis, revision: o.Revision, n: n, status: decision.Status, reason: decision.Reason}}, nil
}

// Suggestion names the testing hypothesis the agent should apply next.
type Suggestion struct {
	HypothesisID int64  `json:"hypothesis_id"`
	Reason       string `json:"reason"`
}

// Suggest draws once from the posterior of every testing hypothesis
// (Thompson sampling) and suggests the one with the largest draw; nil without
// a testing hypothesis.
func Suggest(ctx context.Context, st *store.Store, agentID string, rng *rand.Rand) (*Suggestion, error) {
	lifts, err := st.PlaybookLifts(ctx, agentID)
	if err != nil {
		return nil, err
	}
	hs, post := posteriors(lifts)
	var testing []int
	var drawn []Posterior
	for i, h := range hs {
		if h.status == "testing" {
			testing, drawn = append(testing, i), append(drawn, post[i])
		}
	}
	best := Thompson(drawn, rng)
	if best < 0 {
		return nil, nil
	}
	i := testing[best]
	return &Suggestion{HypothesisID: hs[i].id, Reason: fmt.Sprintf("Apply it to your next action. Thompson draw from its posterior: mean lift %+.2f, sd %.2f after %d counted actions", post[i].Mean, post[i].SD, hs[i].N)}, nil
}
