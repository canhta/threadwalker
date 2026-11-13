package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/canhta/threadz/internal/guard"
	"github.com/canhta/threadz/internal/knowledge"
	"github.com/canhta/threadz/internal/llm"
	"github.com/canhta/threadz/internal/logging"
	"github.com/canhta/threadz/internal/threads"
	"github.com/canhta/threadz/internal/trends"
)

func (d *Dispatcher) executors() map[string]executor {
	tools := map[string]executor{
		"playbook_read": func(ctx context.Context, a arguments) (any, error) {
			return d.opts.Store.PlaybookEntries(ctx, d.opts.Template.ID, int(a.Int("limit")))
		},
		"playbook_propose": func(ctx context.Context, a arguments) (any, error) {
			if id := a.Text("knowledge_id"); id != "" {
				if _, ok, err := knowledge.Get(id); err != nil || !ok {
					return nil, errors.New("proposing playbook entry: unknown knowledge_id")
				}
			}
			entry, err := d.opts.Store.ProposePlaybook(ctx, d.opts.Template.ID, a.Int("hypothesis_id"), a.Text("text"), d.opts.Now(), a.Text("knowledge_id"))
			if err != nil {
				return nil, errors.New("proposing playbook entry: refused")
			}
			return entry, nil
		},
		"knowledge_read": func(ctx context.Context, a arguments) (any, error) {
			entries, err := knowledge.Filter(a.Text("scope"), a.Text("evidence"))
			if err != nil {
				return nil, fmt.Errorf("reading knowledge: %w", err)
			}
			return entries, nil
		},
		"knowledge_candidate": func(ctx context.Context, a arguments) (any, error) {
			id, err := d.opts.Store.AddKnowledgeCandidate(ctx, d.opts.Template.ID, a.Text("technique"), a.Text("url"), d.opts.Now())
			if err != nil {
				return nil, fmt.Errorf("submitting knowledge candidate: %w", err)
			}
			return map[string]int64{"candidate_id": id}, nil
		},
		"strategy_write": func(ctx context.Context, a arguments) (any, error) {
			version, err := d.opts.Store.WriteStrategy(ctx, d.opts.Template.ID, a.Text("text"), a.TextList("citations"), d.opts.Now())
			d.event(ctx, logging.StrategyWritten, outcome(err), reason(err), "strategy_write", time.Time{})
			if err != nil {
				return nil, fmt.Errorf("writing strategy: %w", err)
			}
			return map[string]int64{"version": version}, nil
		},
		"mark_do_not_contact": func(ctx context.Context, a arguments) (any, error) {
			if err := d.opts.Store.MarkDoNotContact(ctx, d.opts.Template.ID, a.Text("username"), "agent_request"); err != nil {
				return nil, errors.New("marking do not contact: failed")
			}
			return map[string]bool{"do_not_contact": true}, nil
		},
		"memory_add": func(ctx context.Context, a arguments) (any, error) {
			if err := d.checkDreamLesson(ctx, a); err != nil {
				return nil, fmt.Errorf("adding memory entry: %w", err)
			}
			err := d.opts.Store.AddMemoryEntry(ctx, d.opts.Template.ID, a.Text("text"), d.opts.Now(), d.memoryActor())
			d.event(ctx, logging.MemoryAdded, outcome(err), reason(err), "memory_add", time.Time{})
			if err != nil {
				return nil, fmt.Errorf("adding memory entry: %w", err)
			}
			if d.opts.Phase == guard.PhaseDream {
				d.lessons++
			}
			return map[string]bool{"added": true}, nil
		},
		"memory_supersede": func(ctx context.Context, a arguments) (any, error) {
			err := d.opts.Store.SupersedeMemory(ctx, d.opts.Template.ID, a.Int("old_id"), a.Int("new_id"), d.opts.Now(), d.memoryActor())
			d.event(ctx, logging.MemorySuperseded, outcome(err), reason(err), "memory_supersede", time.Time{})
			if err != nil {
				return nil, fmt.Errorf("superseding memory entry: %w", err)
			}
			return map[string]bool{"superseded": true}, nil
		},
		"note_write": func(ctx context.Context, a arguments) (any, error) {
			id, err := d.opts.Store.WriteTopic(ctx, d.opts.Template.ID, a.Text("topic"), a.Text("description"), a.Text("text"), d.opts.Now())
			if err == nil {
				d.syncEmbeddings(ctx)
			}
			d.event(ctx, logging.NoteWritten, outcome(err), reason(err), "note_write", time.Time{})
			if err != nil {
				return nil, fmt.Errorf("writing topic note: %w", err)
			}
			return map[string]any{"id": id}, nil
		},
		"note_search": func(ctx context.Context, a arguments) (any, error) {
			hits, err := d.opts.Store.SearchMemory(ctx, d.opts.Template.ID, a.Text("query"), d.embedQuery(ctx, a.Text("query")), a.Time("since"), a.Time("until"), int(a.Int("limit")))
			d.event(ctx, logging.NoteSearched, outcome(err), reason(err), "note_search", time.Time{})
			if err != nil {
				return nil, fmt.Errorf("searching agent notes: %w", err)
			}
			return hits, nil
		},
		"note_read": func(ctx context.Context, a arguments) (any, error) {
			memory, err := d.opts.Store.ReadMemory(ctx, d.opts.Template.ID, a.Text("ref"))
			d.event(ctx, logging.NoteRead, outcome(err), reason(err), "note_read", time.Time{})
			if err != nil {
				return nil, errors.New("reading agent note: not found")
			}
			return memory, nil
		},
		"fetch_trends": func(ctx context.Context, a arguments) (any, error) {
			if d.opts.Phase == guard.PhaseStudy {
				feed, err := d.opts.FetchStudy(ctx)
				d.event(ctx, logging.TrendsFetched, outcome(err), reason(err), "fetch_trends", time.Time{})
				if err != nil {
					return nil, errors.New("fetching study sources: fetch failed")
				}
				stories := trends.RankStories(feed.Items, d.trendProfile)
				d.recordStories(ctx, "study", stories)
				return map[string]any{"stories": trends.TopStories(stories, 10), "failed_sources": feed.Failed, "skipped_sources": feed.Skipped}, nil
			}
			fetched, err := d.opts.FetchTrends(ctx)
			d.event(ctx, logging.TrendsFetched, outcome(err), reason(err), "fetch_trends", time.Time{})
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
				d.guard.NoteTrend(t.Title)
			}
			for i, heat := range trends.StoryHeats(stories, history, now) {
				d.guard.NoteFacts(stories[i].URL, trends.TopicFacts(heat, now))
			}
			d.recordStories(ctx, "agent", stories)
			return ranked, nil
		},
	}
	for _, name := range []string{"read_feed", "read_topic", "search", "profile", "profile_posts", "read_post", "read_replies", "read_own_metrics"} {
		tools[name] = func(ctx context.Context, a arguments) (any, error) {
			if name == "read_own_metrics" && a.Int("action_id") > 0 {
				return d.browserRead(ctx, name, a)
			}
			if name == "read_topic" {
				if err := d.checkTopic(&a); err != nil {
					return nil, err
				}
			}
			data, err := d.browserRead(ctx, name, a)
			if err == nil && name != "read_own_metrics" && name != "profile" {
				d.noteRead(data)
			}
			if err == nil && (name == "search" || name == "read_feed" || name == "read_topic") {
				if page, ok := data.(threads.Page); ok {
					for i, p := range page.Posts {
						page.Posts[i].Ref = d.guard.NoteTrendText(p.URL, p.Text)
					}
					for i, t := range page.Trends {
						page.Trends[i].Ref = d.guard.NoteTrendText(t.URL, t.Title+". "+t.Summary)
						d.guard.NoteTrend(t.Title)
						d.guard.NoteFacts(t.URL, trends.TopicFacts(trends.Heat{Posts: t.PostCount, Rank: i}, d.opts.Now()))
					}
				}
			}
			if page, ok := data.(threads.Page); ok && err == nil && page.LimitedAccess != "" && (name == "search" || name == "profile_posts") {
				// Best-effort: the next wake hides the tool for a day.
				_ = d.opts.Store.RefuseTool(ctx, d.opts.Template.ID, name, d.opts.Now())
			}
			return data, err
		}
	}
	for _, name := range []string{"post", "reply"} {
		tools[name] = func(ctx context.Context, a arguments) (any, error) {
			result, err := d.action(ctx, name, a)
			d.syncEmbeddings(ctx)
			return result, err
		}
	}
	if d.opts.Template.Study.WebSearch {
		tools["web_search"] = func(ctx context.Context, a arguments) (any, error) {
			if d.opts.Phase == guard.PhaseAgent {
				d.webSearches++ // Dispatch already holds d.mu
				if d.webSearches > MaxWebSearchesPerWake {
					return nil, &guard.BlockedError{Reason: "limit_per_wake", Detail: "web_search"}
				}
			}
			result, err := d.webSearch(ctx, a.Text("query"))
			if err == nil {
				for i, s := range result.Sources {
					if refs := d.guard.NoteTrend(s.URL); len(refs) > 0 {
						result.Sources[i].Ref = refs[0]
					}
				}
			}
			d.event(ctx, logging.WebSearchFinished, outcome(err), reason(err), "web_search", time.Time{})
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
				id, err := d.opts.Owner.QueueOwner(ctx, d.opts.Template.ID, a.Text("text"), question)
				if err != nil {
					return nil, errors.New("queueing owner message: failed")
				}
				return map[string]int64{"queued_id": id}, nil
			}
		}
	}
	return tools
}

// recordStories is best-effort: a storage failure never fails the read.
func (d *Dispatcher) recordStories(ctx context.Context, phase string, stories []trends.Story) {
	err := trends.Record(ctx, d.opts.Store, d.opts.Template.ID, phase, stories, d.opts.Now())
	d.event(ctx, logging.TrendsRecorded, outcome(err), reason(err), "fetch_trends", time.Time{})
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
// reported usage to the study phase ledger, so a search is bounded like any
// other study spend.
func (d *Dispatcher) webSearch(ctx context.Context, query string) (trends.WebSearchResult, error) {
	now := d.opts.Now()
	day := now.UTC().Format("2006-01-02")
	spent, err := d.opts.Store.LLMUsageForDayPhases(ctx, d.opts.Template.ID, day, "agent", "study")
	if err != nil {
		return trends.WebSearchResult{}, err
	}
	if spent.CostUSD >= d.opts.Template.Limits.MaxUSDPerDay {
		return trends.WebSearchResult{}, errors.New("web search: daily budget spent")
	}
	result, err := d.opts.SearchWeb(ctx, trends.WebSearchRequest{
		BaseURL:    d.opts.Template.Study.WebSearchBaseURL,
		Model:      d.opts.Template.Study.WebSearchModel,
		APIKey:     d.opts.Template.LLM.APIKey.Expose(),
		Query:      query,
		MaxOutput:  d.opts.Template.Study.WebSearchMaxOutput,
		MaxResults: d.opts.Template.Study.WebSearchMaxResults,
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

// noteRead tells the guard which post id and permalink pairs this wake has seen.
func (d *Dispatcher) noteRead(data any) {
	page, ok := data.(threads.Page)
	if !ok {
		return
	}
	for _, p := range page.Posts {
		d.guard.NoteRead(p.ID, p.URL, p.Text)
		if facts, err := trends.PostFacts(p, d.trendProfile.Include, d.opts.Template.Targets); err == nil && facts.Line != "" {
			d.guard.NoteFacts(p.URL, facts)
		}
	}
}

func (d *Dispatcher) action(ctx context.Context, kind string, a arguments) (any, error) {
	// The guard already validated hypotheses and atomically committed the intent
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
func reason(err error) string {
	if err != nil {
		return "operation_failed"
	}
	return "completed"
}

// embedQuery returns the query's vector, or nil (logging the reason when a
// request failed), which makes SearchMemory rank by BM25 alone for this call.
func (d *Dispatcher) embedQuery(ctx context.Context, query string) []float32 {
	vectors, err := d.opts.Embedder.Embed(ctx, d.opts.Template.ID, d.opts.WakeID, []string{query})
	if err != nil {
		d.event(ctx, logging.EmbeddingFailed, "error", llm.EmbeddingFailureReason(err), "note_search", time.Time{})
		return nil
	}
	return vectors[0]
}

// syncEmbeddings embeds the rows a write just added. A failure is logged and
// leaves them pending for the next sync; the write itself already succeeded.
func (d *Dispatcher) syncEmbeddings(ctx context.Context) {
	_, _ = d.opts.Embedder.Sync(ctx, d.opts.Template.ID, d.opts.WakeID)
}
