package tools

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/canhta/threadz/internal/guard"
	"github.com/canhta/threadz/internal/logging"
	"github.com/canhta/threadz/internal/threads"
	"github.com/canhta/threadz/internal/trends"
)

func (d *Dispatcher) executors() map[string]executor {
	tools := map[string]executor{
		"mark_do_not_contact": func(ctx context.Context, a arguments) (any, error) {
			if err := d.countCall("mark_do_not_contact"); err != nil {
				return nil, err
			}
			if err := d.opts.Store.MarkDoNotContact(ctx, d.opts.Template.ID, a.Text("username"), "agent_request"); err != nil {
				return nil, errors.New("marking do not contact: failed")
			}
			return map[string]bool{"do_not_contact": true}, nil
		},
		"fetch_trends": func(ctx context.Context, a arguments) (any, error) {
			if err := d.countCall("fetch_trends"); err != nil {
				return nil, err
			}
			fetched, err := d.opts.FetchTrends(ctx)
			d.event(ctx, logging.TrendsFetched, outcome(err), failureReason(err), "fetch_trends", time.Time{})
			if err != nil {
				return nil, errors.New("fetching trends: source failed")
			}
			ranked := trends.RankTrends(fetched, d.trendProfile)
			now := d.opts.Now()
			stories := trends.RankStories(ranked, d.trendProfile)
			// Best effort: without history a story has no recency or rank movement.
			history, _ := d.opts.Store.RecentTrendStories(ctx, d.opts.Template.ID, now.Add(-trends.BaselineWindow), 500)
			for i, t := range ranked {
				key := t.URL
				if key == "" {
					key = t.Title
				}
				ranked[i].Ref = d.guard.NoteTrendText(key, t.Title)
			}
			for i, heat := range trends.StoryHeats(stories, history, now) {
				d.guard.NoteFacts(stories[i].URL, trends.TopicFacts(heat, now))
			}
			d.recordStories(ctx, stories)
			return ranked, nil
		},
	}
	tools["find_posts"] = d.findPosts
	for _, name := range []string{"profile", "read_post", "read_replies", "read_own_metrics"} {
		tools[name] = func(ctx context.Context, a arguments) (any, error) {
			data, err := d.browserRead(ctx, name, name, a)
			if err == nil && name != "profile" && name != "read_own_metrics" {
				d.noteRead(ctx, data)
			}
			return data, err
		}
	}
	for _, name := range []string{"post", "reply"} {
		tools[name] = func(ctx context.Context, a arguments) (any, error) {
			return d.action(ctx, name, a)
		}
	}
	if d.opts.Template.WebSearch.Enabled {
		tools["web_search"] = func(ctx context.Context, a arguments) (any, error) {
			if err := d.countCall("web_search"); err != nil {
				return nil, err
			}
			result, err := d.webSearch(ctx, a.Text("query"))
			if err == nil {
				for i, s := range result.Sources {
					if refs := d.guard.NoteTrend(s.URL); len(refs) > 0 {
						result.Sources[i].Ref = refs[0]
					}
				}
			}
			d.event(ctx, logging.WebSearchFinished, outcome(err), trends.WebSearchReason(err), "web_search", time.Time{})
			if err != nil {
				return nil, errors.New("searching the web: failed")
			}
			return result, nil
		}
	}
	if d.opts.Ideas != nil {
		tools["ideas"] = func(ctx context.Context, a arguments) (any, error) {
			ideas, err := d.opts.Ideas(ctx, d.guard.Seen())
			if err != nil {
				return nil, errors.New("choosing ideas: failed")
			}
			return ideas, nil
		}
	}
	if d.opts.Owner != nil {
		for _, name := range []string{"notify_owner", "ask_owner"} {
			question := name == "ask_owner"
			tools[name] = func(ctx context.Context, a arguments) (any, error) {
				if err := d.countCall(name); err != nil {
					return nil, err
				}
				id, err := d.opts.Owner.QueueOwner(ctx, d.opts.Template.ID, a.Text("text"), question)
				if err != nil {
					return nil, errors.New("queueing owner message: failed")
				}
				return map[string]int64{"queued_id": id}, nil
			}
		}
	}
	maps.Copy(tools, d.lessonTools())
	return tools
}

// recordStories is best-effort: a storage failure never fails the read.
func (d *Dispatcher) recordStories(ctx context.Context, stories []trends.Story) {
	err := trends.Record(ctx, d.opts.Store, d.opts.Template.ID, stories, d.opts.Now())
	d.event(ctx, logging.TrendsRecorded, outcome(err), failureReason(err), "fetch_trends", time.Time{})
}

// searchCountry maps the persona's language to where its readers are, so a
// search is localised instead of falling back to the United States.
func searchCountry(language string) string {
	switch strings.ToLower(strings.TrimSpace(language)) {
	case "vi":
		return "VN"
	case "en":
		return "US"
	}
	return ""
}

// webSearch checks the shared daily cap before the call and records the actual
// reported usage to the wake's ledger, so a search is bounded like any other
// spend.
func (d *Dispatcher) webSearch(ctx context.Context, query string) (trends.WebSearchResult, error) {
	now := d.opts.Now()
	day := d.opts.Template.Limits.Day(now)
	spent, err := d.opts.Store.LLMUsageForDayPhases(ctx, d.opts.Template.ID, day, "agent")
	if err != nil {
		return trends.WebSearchResult{}, err
	}
	if spent.CostUSD >= d.opts.Template.Limits.MaxUSDPerDay {
		return trends.WebSearchResult{}, fmt.Errorf("web search: %w", errBudgetSpent)
	}
	result, err := d.opts.SearchWeb(ctx, trends.WebSearchRequest{
		BaseURL:    d.opts.Template.WebSearch.BaseURL,
		Model:      d.opts.Template.WebSearch.Model,
		APIKey:     d.opts.Template.LLM.APIKey.Expose(),
		Query:      query,
		MaxOutput:  d.opts.Template.WebSearch.MaxOutput,
		MaxResults: d.opts.Template.WebSearch.MaxResults,
		Country:    searchCountry(d.opts.Template.Persona.Language),
	})
	if err != nil {
		return trends.WebSearchResult{}, err
	}
	usage := result.Usage
	usage.CostUSD = float64(usage.TokensIn)/1e6*d.opts.Template.LLM.PricePerMTokInUSD + float64(usage.TokensOut)/1e6*d.opts.Template.LLM.PricePerMTokOutUSD
	if err = d.opts.Store.RecordLLMUsage(ctx, d.opts.Template.ID, d.opts.WakeID, day, usage); err != nil {
		return trends.WebSearchResult{}, err
	}
	return result, nil
}

// findPosts is the one finder: it checks the arguments the source takes, the
// source's per-wake cap and whether the source is closed, runs the read, and
// gives every post and trend in the page its ref.
func (d *Dispatcher) findPosts(ctx context.Context, a arguments) (any, error) {
	source := a.Text("source")
	src, _ := guard.FindSourceOf(source)
	scope := guard.FindScope(source)
	given := map[string]bool{"topic": a.Text("topic") != "", "query": a.Text("query") != "", "username": a.Text("username") != "", "recent": a.Bool("recent")}
	for arg, set := range given {
		if set && !slices.Contains(src.Args, arg) {
			return nil, &guard.BlockedError{Reason: "find_args"}
		}
	}
	switch {
	case source == "topic" && given["topic"] == given["query"],
		source == "search" && !given["query"],
		source == "profile" && !given["username"]:
		return nil, &guard.BlockedError{Reason: "find_args"}
	}
	if given["topic"] {
		seen, ok := d.guard.Trend(a.Text("topic"))
		// Only a Threads topic link opens; a news link would fail in the
		// browser and still use up a read.
		if !ok || !threads.IsTopicURL(seen) {
			return nil, &guard.BlockedError{Reason: "find_args"}
		}
		a.Args = a.Args.WithText("url", seen)
	}
	if err := d.countCall(scope); err != nil {
		return nil, err
	}
	data, err := d.browserRead(ctx, scope, src.Read, a)
	if err != nil {
		return nil, err
	}
	d.noteRead(ctx, data)
	if page, ok := data.(threads.Page); ok && page.LimitedAccess != "" && (source == "search" || source == "profile") {
		// Best-effort: the next wake closes this source for a day.
		_ = d.opts.Store.RefuseTool(ctx, d.opts.Template.ID, scope, d.opts.Now())
	}
	return data, nil
}

// noteRead gives every post and trend a read returned its ref: p<id> for a post
// (durable, stored with its figures), t<n> for a trend (this wake only).
func (d *Dispatcher) noteRead(ctx context.Context, data any) {
	page, ok := data.(threads.Page)
	if !ok {
		return
	}
	for i, p := range page.Posts {
		page.Posts[i].Ref = d.guard.NotePost(ctx, p)
	}
	for i, t := range page.Trends {
		page.Trends[i].Ref = d.guard.NoteTrendText(t.URL, t.Title+". "+t.Summary)
		d.guard.NoteFacts(t.URL, trends.TopicFacts(trends.Heat{Posts: t.PostCount, Rank: i}, d.opts.Now()))
	}
}

func (d *Dispatcher) action(ctx context.Context, kind string, a arguments) (any, error) {
	// The guard already validated the action and atomically committed the intent
	// and contact counter. A builtin must never insert a second action.
	if a.ReservedActionID <= 0 {
		return nil, errors.New("recording action: missing guarded reservation")
	}
	if !d.opts.Template.DryRun {
		return d.browserAction(ctx, kind, a)
	}
	return map[string]any{"synthetic": true, "dry_run": true, "action_id": a.ReservedActionID, "parts": 1 + len(a.TextList("parts"))}, nil
}

func outcome(err error) string {
	if err != nil {
		return "error"
	}
	return "success"
}
