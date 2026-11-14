package store

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"
)

// Band is where an episode ranks among comparable actions.
const (
	BandTop      = "top"
	BandMid      = "mid"
	BandBottom   = "bottom"
	BandUnranked = "unranked"
)

const (
	// RankPool is how many of the agent's latest measured actions of one kind
	// an action is ranked against, itself included.
	RankPool = 50
	// MinRankPool is the fewest comparable actions a rank needs.
	MinRankPool = 8
	topRank     = 0.75
	bottomRank  = 0.25
	// ReplyScoreWeight is how many likes one reply is worth in an episode's score.
	ReplyScoreWeight = 2
)

// Episode is one live, confirmed, non-failed own action and what was measured
// at its newest completed metric window.
type Episode struct {
	ActionID                  int64
	Kind                      string
	Text, Hook, Angle, Fact   string
	TargetUsername, ThreadURL string
	PostedAt, MeasuredAt      time.Time
	// LongAt is when the action first had a sample at the 6h window or later.
	LongAt                          time.Time
	ParentReplies, ParentAgeMinutes *int64
	// Window is the completed metric_jobs target in hours: 1, 6 or 24.
	Window                int
	Views, Likes, Replies int64
	// Score is Likes + ReplyScoreWeight*Replies at Window.
	Score int64
	// Rank is the percentile among the pool, 0 to 1; Pool is the pool size.
	// Both are zero when Band is unranked.
	Rank float64
	Pool int
	Band string
}

// EpisodeQuery filters Episodes. Zero values mean no filter.
type EpisodeQuery struct {
	// Since keeps actions posted at or after it.
	Since time.Time
	// Kind is post or reply.
	Kind string
	// Limit keeps the newest Limit episodes.
	Limit int
}

type sample struct{ id, views, score int64 }

// Episodes returns the agent's episodes, newest action first. The window is
// the completed metric_jobs target, never the age at the sample: a late sample
// that completes several targets at once serves each of them. An action is
// ranked against the RankPool newest actions of its kind that have a sample at
// the same window, itself included.
func (s *Store) Episodes(ctx context.Context, agentID string, q EpisodeQuery) ([]Episode, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT a.id,a.kind,a.text,a.hook,a.angle,a.fact,a.target_username,a.thread_url,a.posted_at,a.parent_replies,a.parent_age_minutes,j.window_hours,m.measured_at,m.views,m.likes,m.replies
		FROM actions a
		JOIN metric_jobs j ON j.agent_id=a.agent_id AND j.action_id=a.id AND j.completed_at IS NOT NULL AND j.reason=''
		JOIN metrics m ON m.action_id=a.id AND m.measured_at=j.completed_at
		WHERE a.agent_id=? AND a.dry_run=0 AND a.failed=0 AND a.thread_url<>'' AND a.media_id<>''
		ORDER BY a.id`, agentID)
	if err != nil {
		return nil, fmt.Errorf("reading episodes: %w", err)
	}
	defer rows.Close()
	newest := map[int64]*Episode{}
	longAt := map[int64]time.Time{}
	var order []int64
	type poolKey struct {
		kind   string
		window int
	}
	pools := map[poolKey][]sample{}
	for rows.Next() {
		var e Episode
		var posted, measured string
		if err = rows.Scan(&e.ActionID, &e.Kind, &e.Text, &e.Hook, &e.Angle, &e.Fact, &e.TargetUsername, &e.ThreadURL, &posted, &e.ParentReplies, &e.ParentAgeMinutes, &e.Window, &measured, &e.Views, &e.Likes, &e.Replies); err != nil {
			return nil, fmt.Errorf("reading episode: %w", err)
		}
		if e.PostedAt, err = parseTime(posted); err != nil {
			return nil, fmt.Errorf("reading episode %d: %w", e.ActionID, err)
		}
		if e.MeasuredAt, err = parseTime(measured); err != nil {
			return nil, fmt.Errorf("reading episode %d: %w", e.ActionID, err)
		}
		e.Score = e.Likes + ReplyScoreWeight*e.Replies
		if at, ok := longAt[e.ActionID]; e.Window >= 6 && (!ok || e.MeasuredAt.Before(at)) {
			longAt[e.ActionID] = e.MeasuredAt
		}
		k := poolKey{e.Kind, e.Window}
		pools[k] = append(pools[k], sample{e.ActionID, e.Views, e.Score})
		if prev, ok := newest[e.ActionID]; !ok {
			order = append(order, e.ActionID)
			newest[e.ActionID] = &e
		} else if e.Window > prev.Window {
			*prev = e
		}
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("reading episodes: %w", err)
	}
	var out []Episode
	for i := len(order) - 1; i >= 0; i-- {
		e := newest[order[i]]
		e.LongAt = longAt[e.ActionID]
		if (!q.Since.IsZero() && e.PostedAt.Before(q.Since)) || (q.Kind != "" && e.Kind != q.Kind) {
			continue
		}
		pool := pools[poolKey{e.Kind, e.Window}]
		pool = pool[max(len(pool)-RankPool, 0):]
		if pool[0].id > e.ActionID {
			pool = append([]sample{{e.ActionID, e.Views, e.Score}}, pool...)
		}
		e.Rank, e.Pool, e.Band = rank(pool, e.Views, e.Score)
		out = append(out, *e)
		if q.Limit > 0 && len(out) == q.Limit {
			break
		}
	}
	return out, nil
}

// rank places a score, ties broken by views, among the pool, which contains it.
// Identical entries count half.
func rank(pool []sample, views, score int64) (float64, int, string) {
	if len(pool) < MinRankPool {
		return 0, 0, BandUnranked
	}
	below, same := 0.0, -1.0 // the pool holds the action itself
	for _, p := range pool {
		switch c := cmp.Or(cmp.Compare(p.score, score), cmp.Compare(p.views, views)); {
		case c < 0:
			below++
		case c == 0:
			same++
		}
	}
	r := (below + same/2) / float64(len(pool)-1)
	switch {
	case r >= topRank:
		return r, len(pool), BandTop
	case r <= bottomRank:
		return r, len(pool), BandBottom
	}
	return r, len(pool), BandMid
}

// Best returns the n highest ranked episodes, best first.
func Best(eps []Episode, n int) []Episode { return byRank(eps, n, -1) }

// Worst returns the n lowest ranked episodes, worst first.
func Worst(eps []Episode, n int) []Episode { return byRank(eps, n, 1) }

func byRank(eps []Episode, n, dir int) []Episode {
	ranked := make([]Episode, 0, len(eps))
	for _, e := range eps {
		if e.Band != BandUnranked {
			ranked = append(ranked, e)
		}
	}
	slices.SortStableFunc(ranked, func(a, b Episode) int { return dir * cmp.Compare(a.Rank, b.Rank) })
	return ranked[:min(n, len(ranked))]
}
