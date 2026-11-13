package trends

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/store"
)

// BuildBrief reads the stored rows for one agent and computes its trend brief.
func BuildBrief(ctx context.Context, st *store.Store, tmpl config.Template, ownUsernames []string, now time.Time) (Brief, error) {
	in, err := loadInput(ctx, st, tmpl, ownUsernames, now)
	if err != nil {
		return Brief{}, err
	}
	return Compute(in), nil
}

func loadInput(ctx context.Context, st *store.Store, tmpl config.Template, ownUsernames []string, now time.Time) (Input, error) {
	observations, err := st.RecentThreadsObservations(ctx, tmpl.ID, now.Add(-BaselineWindow), 1000)
	if err != nil {
		return Input{}, err
	}
	stories, err := st.RecentStudyTrends(ctx, tmpl.ID, now.Add(-BaselineWindow), 50)
	if err != nil {
		return Input{}, err
	}
	targets, err := st.ReplyTargets(ctx, tmpl.ID)
	if err != nil {
		return Input{}, err
	}
	replied := map[string]bool{}
	for _, target := range targets {
		if canonical, err := store.CanonicalThread(target); err == nil {
			replied[canonical] = true
		}
	}
	include, avoid := tmpl.TrendKeywords()
	dnc := map[string]bool{}
	seen := map[string]bool{}
	for _, observation := range observations {
		if len(seen) == 50 {
			break
		}
		if seen[observation.Author] {
			continue
		}
		seen[observation.Author] = true
		blocked, err := st.IsDoNotContact(ctx, tmpl.ID, observation.Author)
		if err == nil && blocked {
			dnc[observation.Author] = true
		}
	}
	return Input{Observations: observations, Stories: stories, Include: include, Avoid: avoid, OwnUsernames: ownUsernames, DoNotContact: dnc, Replied: replied, Targets: tmpl.Targets, Now: now}, nil
}

// PostingContext is what the stored observations say about an action when it is
// reserved. A nil field was not observed; nothing is invented for it.
type PostingContext struct {
	// TargetScoreJSON and DelayMinutes describe a reply's target: the numeric
	// score parts and the minutes since its story was first seen.
	TargetScoreJSON string
	DelayMinutes    *int64
	// TopicHeat is the highest brief heat of a story or rising keyword the
	// action text shares at least two topic tokens (one for a keyword) with; 0
	// when the brief has signals and none match.
	TopicHeat *float64
	// ParentReplies and ParentAgeMinutes describe the replied-to post.
	ParentReplies, ParentAgeMinutes *int64
}

// ContextOf computes the posting context of an action from the stored observations.
func ContextOf(ctx context.Context, st *store.Store, tmpl config.Template, ownUsernames []string, text, targetURL string, now time.Time) (PostingContext, error) {
	in, err := loadInput(ctx, st, tmpl, ownUsernames, now)
	if err != nil {
		return PostingContext{}, err
	}
	var pc PostingContext
	brief := Compute(in)
	if len(brief.Stories)+len(brief.Rising) > 0 {
		tokens := config.TopicTokens(text)
		heat := 0.0
		for _, story := range brief.Stories {
			if sharedTokens(tokens, config.TopicTokens(story.Title)) >= 2 {
				heat = max(heat, story.Heat)
			}
		}
		for _, keyword := range brief.Rising {
			if sharedTokens(tokens, config.TopicTokens(keyword.Keyword)) >= 1 {
				heat = max(heat, keyword.Heat)
			}
		}
		pc.TopicHeat = &heat
	}
	want, err := store.CanonicalThread(targetURL)
	if err != nil {
		return pc, nil
	}
	perPost, regular, angles := groupPosts(in)
	for url, list := range perPost {
		if canonical, err := store.CanonicalThread(url); err != nil || canonical != want {
			continue
		}
		if _, ok := regular[url]; !ok {
			continue
		}
		hot, _ := hotPostOf(list, in, angles)
		first := hot.firstSeen
		tokens := config.TopicTokens(hot.Snippet)
		for _, story := range in.Stories {
			if story.ObservedAt.Before(first) && sharedTokens(tokens, config.TopicTokens(story.Title)) >= 2 {
				first = story.ObservedAt
			}
		}
		encoded, err := json.Marshal(hot.Target)
		if err != nil {
			return pc, fmt.Errorf("encoding reply target score: %w", err)
		}
		delay := int64(now.Sub(first).Minutes())
		age := hot.Target.AgeMinutes
		pc.TargetScoreJSON, pc.DelayMinutes, pc.ParentReplies, pc.ParentAgeMinutes = string(encoded), &delay, hot.Target.Replies, &age
		return pc, nil
	}
	return pc, nil
}

func sharedTokens(a, b []string) int {
	set := map[string]bool{}
	for _, token := range b {
		set[token] = true
	}
	n := 0
	for _, token := range a {
		if set[token] {
			n++
		}
	}
	return n
}

// Spiked reports whether a story seen in the last two hours scores above
// multiplier times the median of the older ones. Zero multiplier and minScore
// select 2 and 6; fewer than three older stories never spike.
func Spiked(stories []store.TrendStory, now time.Time, multiplier float64, minScore int) bool {
	recent, baseline := []int{}, []int{}
	for _, story := range stories {
		if now.Sub(story.ObservedAt) <= 2*time.Hour {
			recent = append(recent, story.Score)
		} else {
			baseline = append(baseline, story.Score)
		}
	}
	if len(recent) == 0 || len(baseline) < 3 {
		return false
	}
	if multiplier <= 0 {
		multiplier = 2
	}
	if minScore <= 0 {
		minScore = 6
	}
	maxRecent := 0
	for _, score := range recent {
		if score > maxRecent {
			maxRecent = score
		}
	}
	sort.Ints(baseline)
	median := baseline[len(baseline)/2]
	return maxRecent >= minScore && float64(maxRecent) > multiplier*float64(median)+1
}
