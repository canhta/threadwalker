// Package dashboard projects a bounded public view of local agent activity.
// Mutable content, credentials and unbounded database rows are never a view
// model. One route serves the whole page; static assets are embedded.
package dashboard

import (
	"context"
	"errors"
	"github.com/canhta/threadz/internal/stdx"
	"log/slog"
	"strings"
	"time"

	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/ratelimit"
	"github.com/canhta/threadz/internal/store"
)

const (
	RowLimit   = 20
	AgentLimit = 100

	defaultCacheTTL   = 10 * time.Second
	defaultStaleTTL   = 2 * time.Minute
	defaultCacheMax   = 256
	defaultRate       = 40
	defaultBurst      = 80
	defaultMaxBuckets = 4096
	defaultConcurrent = 16
	defaultConns      = 64
)

// Agent is explicitly selected operator metadata. Username is used solely to
// prove publication-link ownership. Timezone is the owner's display zone.
type Agent struct {
	ID             string
	Username       string
	DryRun         bool
	DailyBudgetUSD float64
	Timezone       string
	Location       *time.Location
	// Limits.Timezone is schedule.timezone: the zone of "today" and of the day columns.
	Limits config.LimitsConfig
}

// Source reads the stored facts one page needs. The store implements it.
type Source interface {
	DashboardSnapshot(ctx context.Context, q store.DashboardQuery) (store.DashboardSnapshot, error)
}

type Options struct {
	Source Source
	Agents []Agent
	Now    func() time.Time
	Logger *slog.Logger

	CacheTTL       time.Duration
	StaleTTL       time.Duration
	CacheMax       int
	RatePerSec     float64
	RateBurst      int
	MaxConcurrent  int
	MaxConnections int
}

type Handler struct {
	source   Source
	agents   []Agent
	now      func() time.Time
	logger   *slog.Logger
	cache    *pageCache
	limiter  *ratelimit.Limiter
	sem      chan struct{}
	maxConns int
	ttl      time.Duration
}

func New(o Options) (*Handler, error) {
	if o.Source == nil || o.Now == nil || len(o.Agents) == 0 || len(o.Agents) > AgentLimit {
		return nil, errors.New("creating dashboard: source, clock and bounded agent selection required")
	}
	seen := map[string]bool{}
	agents := make([]Agent, 0, len(o.Agents))
	for _, a := range o.Agents {
		if a.ID == "" || len(a.ID) > 128 || seen[a.ID] || !stdx.FiniteNonNegative(a.DailyBudgetUSD) || a.DailyBudgetUSD <= 0 {
			return nil, errors.New("creating dashboard: invalid public agent configuration")
		}
		seen[a.ID] = true
		loc := time.UTC
		if strings.TrimSpace(a.Timezone) != "" {
			var err error
			loc, err = time.LoadLocation(strings.TrimSpace(a.Timezone))
			if err != nil {
				return nil, errors.New("creating dashboard: invalid agent timezone")
			}
		}
		a.Location = loc
		agents = append(agents, a)
	}
	cacheTTL := o.CacheTTL
	if cacheTTL <= 0 {
		cacheTTL = defaultCacheTTL
	}
	staleTTL := o.StaleTTL
	if staleTTL < 0 {
		staleTTL = 0
	} else if staleTTL == 0 {
		staleTTL = defaultStaleTTL
	}
	cacheMax := o.CacheMax
	if cacheMax <= 0 {
		cacheMax = defaultCacheMax
	}
	rate := o.RatePerSec
	if rate <= 0 {
		rate = defaultRate
	}
	burst := o.RateBurst
	if burst <= 0 {
		burst = defaultBurst
	}
	concurrent := o.MaxConcurrent
	if concurrent <= 0 {
		concurrent = defaultConcurrent
	}
	conns := o.MaxConnections
	if conns <= 0 {
		conns = defaultConns
	}
	now := o.Now
	return &Handler{
		source:   o.Source,
		agents:   agents,
		now:      now,
		logger:   o.Logger,
		cache:    newPageCache(cacheTTL, staleTTL, cacheMax, now),
		limiter:  ratelimit.New(rate, float64(burst), defaultMaxBuckets, now),
		sem:      make(chan struct{}, concurrent),
		maxConns: conns,
		ttl:      cacheTTL,
	}, nil
}

// Page is the default public projection used by offline evaluation and tests.
func (h *Handler) Page(ctx context.Context) (Page, error) {
	p, err := h.readPage(ctx, h.agents[0], 7, postView{}, h.now().UTC())
	if err != nil {
		return Page{}, queryError(ctx)
	}
	return p, nil
}

func (h *Handler) readPage(ctx context.Context, a Agent, days int, view postView, now time.Time) (Page, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	snap, err := h.source.DashboardSnapshot(ctx, store.DashboardQuery{AgentID: a.ID, From: dayStarts(a.Limits, now, days)[0], Now: now})
	if err != nil {
		return Page{}, err
	}
	return buildPage(snap, h.agents, a, days, view, now)
}

func (h *Handler) agent(id string) (Agent, bool) {
	for _, a := range h.agents {
		if a.ID == id {
			return a, true
		}
	}
	return Agent{}, false
}

func queryError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return errors.Join(errors.New("reading dashboard: cancelled"), err)
	}
	return errors.New("reading dashboard: state unavailable")
}

func oneOf(s string, values ...string) string {
	for _, v := range values {
		if s == v {
			return v
		}
	}
	return "unknown"
}
