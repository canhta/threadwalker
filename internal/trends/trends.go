// Package trends computes deterministic Threads-native trend signals from
// bounded read observations. It makes no network or model calls.
package trends

import (
	"math"
	"sort"
	"strings"
	"time"

	"github.com/canhta/threadwalker/internal/config"
	"github.com/canhta/threadwalker/internal/store"
)

// KeywordTrend is a rising Threads keyword in the recent window versus its
// older baseline.
type KeywordTrend struct {
	Keyword  string  `json:"keyword"`
	Recent   int     `json:"recent_posts"`
	Baseline int     `json:"baseline_posts"`
	Score    float64 `json:"score"`
}

// HotPost is one post worth attention, with visible reply velocity.
type HotPost struct {
	URL           string  `json:"url"`
	Author        string  `json:"author"`
	Snippet       string  `json:"snippet"`
	Replies       int64   `json:"replies"`
	ReplyVelocity float64 `json:"reply_velocity_per_hour"`
	AgeHours      float64 `json:"age_hours"`
	OnTopic       bool    `json:"on_topic"`
	ThreadsOnly   bool    `json:"threads_only"`
}

// FormatStat counts observed formats ("short", "medium+question", ...).
type FormatStat struct {
	Format string `json:"format"`
	Posts  int    `json:"posts"`
}

// MergedStory combines external study clusters with Threads signals.
type MergedStory struct {
	Title   string   `json:"title"`
	URL     string   `json:"url"`
	Sources []string `json:"sources"`
	Score   float64  `json:"score"`
	// Coverage is "both", "external_only" (early chance to be first) or
	// "threads_only" (local meme).
	Coverage string `json:"coverage"`
}

// Brief is the compact trend brief for a wake or dream context.
type Brief struct {
	GeneratedAt time.Time      `json:"generated_at"`
	Rising      []KeywordTrend `json:"rising_keywords"`
	HotPosts    []HotPost      `json:"hot_posts"`
	Formats     []FormatStat   `json:"formats"`
	Stories     []MergedStory  `json:"stories"`
}

// Input bundles the bounded observations and story rows for one brief.
type Input struct {
	Observations []store.ThreadsObservation
	Stories      []store.TrendStory
	Include      []string
	Avoid        []string
	OwnUsernames []string
	DoNotContact map[string]bool
	Now          time.Time
}

const (
	recentWindow   = 24 * time.Hour
	baselineWindow = 7 * 24 * time.Hour
	maxKeywords    = 10
	maxHotPosts    = 5
	maxMerged      = 10
)

// Compute builds the brief deterministically from stored rows.
func Compute(in Input) Brief {
	now := in.Now
	brief := Brief{GeneratedAt: now, Rising: []KeywordTrend{}, HotPosts: []HotPost{}, Formats: []FormatStat{}, Stories: []MergedStory{}}
	avoid := map[string]bool{}
	for _, keyword := range in.Avoid {
		for _, token := range strings.Fields(strings.ToLower(keyword)) {
			avoid[token] = true
		}
	}
	recent := map[string]map[string]bool{}
	baseline := map[string]map[string]bool{}
	for _, o := range in.Observations {
		age := now.Sub(o.ObservedAt)
		if age < 0 || age > baselineWindow {
			continue
		}
		tokens := config.TopicTokens(o.Snippet)
		bucket := baseline
		if age <= recentWindow {
			bucket = recent
		}
		for _, token := range tokens {
			if avoid[token] || len([]rune(token)) < 2 {
				continue
			}
			if bucket[token] == nil {
				bucket[token] = map[string]bool{}
			}
			bucket[token][o.PostURL] = true
		}
	}
	keys := map[string]bool{}
	for token := range recent {
		keys[token] = true
	}
	for token := range baseline {
		keys[token] = true
	}
	for token := range keys {
		recentN, baselineN := len(recent[token]), len(baseline[token])
		if recentN+baselineN < 2 || recentN == 0 {
			continue
		}
		score := float64(recentN) / (float64(baselineN)/6 + 0.5)
		brief.Rising = append(brief.Rising, KeywordTrend{Keyword: token, Recent: recentN, Baseline: baselineN, Score: math.Round(score*100) / 100})
	}
	sort.SliceStable(brief.Rising, func(i, j int) bool {
		if brief.Rising[i].Score != brief.Rising[j].Score {
			return brief.Rising[i].Score > brief.Rising[j].Score
		}
		return brief.Rising[i].Keyword < brief.Rising[j].Keyword
	})
	if len(brief.Rising) > maxKeywords {
		brief.Rising = brief.Rising[:maxKeywords]
	}

	// Group observations per post in time order for velocity.
	perPost := map[string][]store.ThreadsObservation{}
	for _, o := range in.Observations {
		age := now.Sub(o.ObservedAt)
		if age < 0 || age > baselineWindow || o.PostURL == "" {
			continue
		}
		perPost[o.PostURL] = append(perPost[o.PostURL], o)
	}
	regular := map[string]store.ThreadsObservation{}
	for url, list := range perPost {
		sort.Slice(list, func(i, j int) bool { return list[i].ObservedAt.Before(list[j].ObservedAt) })
		perPost[url] = list
	}
	own := map[string]bool{}
	for _, username := range in.OwnUsernames {
		own[strings.ToLower(strings.TrimPrefix(username, "@"))] = true
	}
	for url, list := range perPost {
		latest := list[len(list)-1]
		if in.DoNotContact[strings.ToLower(latest.Author)] || own[strings.ToLower(latest.Author)] {
			continue
		}
		// One representative observation per post for format counts.
		regular[url] = latest
		if len(list) < 2 || list[0].Replies == nil || latest.Replies == nil {
			continue
		}
		hours := latest.ObservedAt.Sub(list[0].ObservedAt).Hours()
		if hours <= 0 {
			continue
		}
		delta := *latest.Replies - *list[0].Replies
		if delta <= 0 {
			continue
		}
		brief.HotPosts = append(brief.HotPosts, HotPost{
			URL: latest.PostURL, Author: latest.Author, Snippet: latest.Snippet,
			Replies: *latest.Replies, ReplyVelocity: math.Round(float64(delta)/hours*100) / 100,
			AgeHours: math.Round(now.Sub(latest.ObservedAt).Hours()*10) / 10,
			OnTopic:  onTopic(latest.Snippet, in.Include),
		})
	}
	sort.SliceStable(brief.HotPosts, func(i, j int) bool {
		if brief.HotPosts[i].ReplyVelocity != brief.HotPosts[j].ReplyVelocity {
			return brief.HotPosts[i].ReplyVelocity > brief.HotPosts[j].ReplyVelocity
		}
		return brief.HotPosts[i].URL < brief.HotPosts[j].URL
	})
	if len(brief.HotPosts) > maxHotPosts {
		brief.HotPosts = brief.HotPosts[:maxHotPosts]
	}

	formats := map[string]int{}
	for _, o := range regular {
		name := "short"
		length := len([]rune(o.Snippet))
		switch {
		case length > 280:
			name = "long"
		case length > 140:
			name = "medium"
		}
		if strings.Contains(o.Snippet, "?") {
			name += "+question"
		}
		if strings.Contains(strings.ToLower(o.Snippet), "http") {
			name += "+link"
		}
		formats[name]++
	}
	names := make([]string, 0, len(formats))
	for name := range formats {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		brief.Formats = append(brief.Formats, FormatStat{Format: name, Posts: formats[name]})
	}

	// Merge external clusters with Threads signals.
	external := []store.TrendStory{}
	seenTitles := map[string]bool{}
	for _, story := range in.Stories {
		key := strings.ToLower(strings.TrimSpace(story.Title))
		if key == "" || seenTitles[key] {
			continue
		}
		seenTitles[key] = true
		external = append(external, story)
	}
	threadTokens := map[string]bool{}
	for _, o := range regular {
		for _, token := range config.TopicTokens(o.Snippet) {
			threadTokens[token] = true
		}
	}
	for _, story := range external {
		coverage := "external_only"
		shared := 0
		for _, token := range config.TopicTokens(story.Title) {
			if threadTokens[token] {
				shared++
			}
		}
		if shared >= 2 {
			coverage = "both"
		}
		brief.Stories = append(brief.Stories, MergedStory{Title: story.Title, URL: story.URL, Sources: splitSources(story.Source), Score: float64(story.Score), Coverage: coverage})
	}
	externalTokens := []map[string]bool{}
	for _, story := range external {
		set := map[string]bool{}
		for _, token := range config.TopicTokens(story.Title) {
			set[token] = true
		}
		externalTokens = append(externalTokens, set)
	}
	for _, hot := range brief.HotPosts {
		matched := false
		hotTokens := map[string]bool{}
		for _, token := range config.TopicTokens(hot.Snippet) {
			hotTokens[token] = true
		}
		for _, set := range externalTokens {
			shared := 0
			for token := range hotTokens {
				if set[token] {
					shared++
				}
			}
			if shared >= 2 {
				matched = true
				break
			}
		}
		if !matched {
			brief.Stories = append(brief.Stories, MergedStory{Title: hot.Snippet, URL: hot.URL, Sources: []string{"threads"}, Score: hot.ReplyVelocity, Coverage: "threads_only"})
		}
	}
	sort.SliceStable(brief.Stories, func(i, j int) bool {
		if brief.Stories[i].Score != brief.Stories[j].Score {
			return brief.Stories[i].Score > brief.Stories[j].Score
		}
		return brief.Stories[i].Title < brief.Stories[j].Title
	})
	if len(brief.Stories) > maxMerged {
		brief.Stories = brief.Stories[:maxMerged]
	}
	return brief
}

func onTopic(text string, include []string) bool {
	lower := strings.ToLower(text)
	for _, keyword := range include {
		if keyword != "" && strings.Contains(lower, strings.ToLower(keyword)) {
			return true
		}
	}
	return false
}

func splitSources(value string) []string {
	out := []string{}
	for _, part := range strings.Split(value, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	if len(out) == 0 {
		out = []string{"external"}
	}
	return out
}
