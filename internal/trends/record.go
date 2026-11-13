package trends

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/store"
)

const maxRecorded = 10

// Record keeps a bounded snapshot of ranked stories for the next normal wake
// and the spike check.
func Record(ctx context.Context, st *store.Store, templateID, phase string, stories []Story, now time.Time) error {
	records := make([]store.TrendStory, 0, maxRecorded)
	for _, story := range stories {
		if len(records) == maxRecorded {
			break
		}
		records = append(records, store.TrendStory{ObservedAt: now, Phase: phase, Source: strings.Join(story.Sources, ","), Title: story.Title, URL: story.URL, Score: story.Score})
	}
	return st.RecordTrendStories(ctx, templateID, phase, records, now, maxRecorded)
}

// Refresh fetches the outside trends and replaces the agent's snapshot exactly
// as a wake's fetch_trends call would. It returns the number of stories kept.
func Refresh(ctx context.Context, st *store.Store, t config.Template, fetch func(context.Context) ([]Trend, error), now time.Time) (int, error) {
	fetched, err := fetch(ctx)
	if err != nil {
		return 0, errors.New("refreshing trends: fetch failed")
	}
	include, avoid := t.TrendKeywords()
	profile := TrendProfile{Include: include, Avoid: avoid}
	stories := RankStories(RankTrends(fetched, profile), profile)
	if err = Record(ctx, st, t.ID, "agent", stories, now); err != nil {
		return 0, fmt.Errorf("refreshing trends: %w", err)
	}
	return min(len(stories), maxRecorded), nil
}
