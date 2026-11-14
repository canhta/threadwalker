// Package trends computes deterministic Threads-native trend signals from
// bounded read observations. It makes no network or model calls.
package trends

import (
	"math"
	"sort"
	"strings"
	"time"

	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/store"
)

// KeywordTrend is a rising Threads keyword in the recent window versus its
// older baseline.
type KeywordTrend struct {
	Keyword  string  `json:"keyword"`
	Recent   int     `json:"recent_posts"`
	Baseline int     `json:"baseline_posts"`
	Score    float64 `json:"score"`
	StorySignal
}

// HotPost is one post worth attention, with visible reply velocity.
type HotPost struct {
	URL           string  `json:"url"`
	Author        string  `json:"author"`
	Snippet       string  `json:"snippet"`
	Replies       int64   `json:"replies"`
	ReplyVelocity float64 `json:"reply_velocity_per_hour"`
	AgeHours      float64 `json:"age_hours"`
	ThreadsOnly   bool    `json:"threads_only"`
	// Target scores this post as a reply target; SizeTier is transient and
	// never persisted.
	Target   TargetScore `json:"target"`
	SizeTier string      `json:"size_tier"`
	// firstSeen is the post's first observation.
	firstSeen time.Time
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
	StorySignal
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
	Avoid        []string
	OwnUsernames []string
	DoNotContact map[string]bool
	// Replied holds the CanonicalThread form of every thread we replied in.
	Replied map[string]bool
	// OwnFollowers and AuthorFollowers (by lowercase username) are zero or
	// absent until a follower source exists; the size tier is then unknown.
	Targets         config.TargetsConfig
	OwnFollowers    int64
	AuthorFollowers map[string]int64
	Now             time.Time
}

const (
	recentWindow = 24 * time.Hour
	// BaselineWindow is the longest history the brief reads.
	BaselineWindow = 7 * 24 * time.Hour
	maxKeywords    = 10
	maxHotPosts    = 5
	maxMerged      = 10
	// A keyword's velocity and acceleration come from its distinct posts in
	// three consecutive buckets of the recent window.
	keywordBuckets   = 3
	keywordBucket    = recentWindow / keywordBuckets
	keywordScoreHalf = 3.0
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
	type keywordHistory struct {
		first   time.Time
		buckets [keywordBuckets]map[string]bool
	}
	history := map[string]*keywordHistory{}
	for _, o := range in.Observations {
		age := now.Sub(o.ObservedAt)
		if age < 0 || age > BaselineWindow {
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
			h := history[token]
			if h == nil {
				h = &keywordHistory{first: o.ObservedAt}
				history[token] = h
			}
			if o.ObservedAt.Before(h.first) {
				h.first = o.ObservedAt
			}
			if age <= recentWindow {
				b := keywordBuckets - 1 - int(age/keywordBucket)
				if b < 0 {
					b = 0
				}
				if h.buckets[b] == nil {
					h.buckets[b] = map[string]bool{}
				}
				h.buckets[b][o.PostURL] = true
			}
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
		h := history[token]
		samples := make([]sample, keywordBuckets)
		for i := range samples {
			samples[i] = sample{now.Add(-time.Duration(keywordBuckets-1-i) * keywordBucket), float64(len(h.buckets[i]))}
		}
		signal := signalOf(samples, h.first, score, keywordScoreHalf, recentN, 0, now)
		brief.Rising = append(brief.Rising, KeywordTrend{Keyword: token, Recent: recentN, Baseline: baselineN, Score: math.Round(score*100) / 100, StorySignal: signal})
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

	perPost, regular, angles := groupPosts(in)
	replySamples := map[string][]sample{}
	for url := range regular {
		hot, samples := hotPostOf(perPost[url], in, angles)
		replySamples[url] = samples
		if hot.ReplyVelocity > 0 {
			brief.HotPosts = append(brief.HotPosts, hot)
		}
	}
	sort.SliceStable(brief.HotPosts, func(i, j int) bool {
		if a, b := brief.HotPosts[i].Target.Score, brief.HotPosts[j].Target.Score; a != b {
			return a > b
		}
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

	external := []store.TrendStory{}
	seenTitles := map[string]bool{}
	storySamples := map[string][]sample{}
	storyFirst := map[string]time.Time{}
	for _, story := range in.Stories {
		key := strings.ToLower(strings.TrimSpace(story.Title))
		if key == "" {
			continue
		}
		storySamples[key] = append(storySamples[key], sample{story.ObservedAt, float64(story.Score)})
		if first, ok := storyFirst[key]; !ok || story.ObservedAt.Before(first) {
			storyFirst[key] = story.ObservedAt
		}
		if seenTitles[key] {
			continue
		}
		seenTitles[key] = true
		external = append(external, story)
	}
	snippets := map[string]string{}
	for url, o := range regular {
		snippets[url] = o.Snippet
	}
	postsExcept := func(skip string) []string {
		out := make([]string, 0, len(snippets))
		for url, snippet := range snippets {
			if url != skip {
				out = append(out, snippet)
			}
		}
		return out
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
		key := strings.ToLower(strings.TrimSpace(story.Title))
		saturation, sameAngle := saturationOf(config.TopicTokens(story.Title), postsExcept(""))
		signal := signalOf(storySamples[key], storyFirst[key], float64(story.Score), externalScoreHalf, saturation, sameAngle, now)
		brief.Stories = append(brief.Stories, MergedStory{Title: story.Title, URL: story.URL, Sources: splitSources(story.Source), Score: float64(story.Score), Coverage: coverage, StorySignal: signal})
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
			saturation, sameAngle := saturationOf(config.TopicTokens(hot.Snippet), postsExcept(hot.URL))
			signal := signalOf(replySamples[hot.URL], hot.firstSeen, hot.ReplyVelocity, repliesHalf, saturation, sameAngle, now)
			brief.Stories = append(brief.Stories, MergedStory{Title: hot.Snippet, URL: hot.URL, Sources: []string{"threads"}, Score: hot.ReplyVelocity, Coverage: "threads_only", StorySignal: signal})
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

// groupPosts groups observations per post in time order, and picks the posts
// of other, contactable authors as regular with their bigrams.
func groupPosts(in Input) (perPost map[string][]store.ThreadsObservation, regular map[string]store.ThreadsObservation, angles map[string]map[bigram]bool) {
	now := in.Now
	perPost = map[string][]store.ThreadsObservation{}
	for _, o := range in.Observations {
		age := now.Sub(o.ObservedAt)
		if age < 0 || age > BaselineWindow || o.PostURL == "" {
			continue
		}
		perPost[o.PostURL] = append(perPost[o.PostURL], o)
	}
	regular = map[string]store.ThreadsObservation{}
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
		regular[url] = latest
	}
	angles = map[string]map[bigram]bool{}
	for url, o := range regular {
		angles[url] = bigrams(config.TopicTokens(o.Snippet))
	}
	return perPost, regular, angles
}

// hotPostOf describes one post from its time-ordered observations: reply
// velocity (zero without two counted observations) and its reply-target score.
// Age counts from the first observation, the earliest sighting there is.
func hotPostOf(list []store.ThreadsObservation, in Input, angles map[string]map[bigram]bool) (HotPost, []sample) {
	first, latest := list[0], list[len(list)-1]
	var samples []sample
	for _, o := range list {
		if o.Replies != nil {
			samples = append(samples, sample{o.ObservedAt, float64(*o.Replies)})
		}
	}
	velocity := 0.0
	if len(samples) >= 2 {
		if hours := samples[len(samples)-1].At.Sub(samples[0].At).Hours(); hours > 0 {
			velocity = math.Max(0, (samples[len(samples)-1].Value-samples[0].Value)/hours)
		}
	}
	hot := HotPost{
		URL: latest.PostURL, Author: latest.Author, Snippet: latest.Snippet,
		ReplyVelocity: math.Round(velocity*100) / 100,
		AgeHours:      math.Round(in.Now.Sub(latest.ObservedAt).Hours()*10) / 10,
		firstSeen:     first.ObservedAt,
	}
	if latest.Replies != nil {
		hot.Replies = *latest.Replies
	}
	canonical, err := store.CanonicalThread(latest.PostURL)
	if err != nil {
		canonical = latest.PostURL
	}
	joke := 0
	for url, other := range angles {
		if url == latest.PostURL {
			continue
		}
		for b := range angles[latest.PostURL] {
			if other[b] {
				joke++
				break
			}
		}
	}
	hot.SizeTier = sizeTier(in.AuthorFollowers[strings.ToLower(latest.Author)], in.OwnFollowers)
	hot.Target = scoreTarget(TargetParts{
		ReplyVelocity: hot.ReplyVelocity, AgeMinutes: int64(in.Now.Sub(first.ObservedAt).Minutes()),
		AlreadyReplied: in.Replied[canonical], JokeSaturation: joke, Replies: latest.Replies, Likes: latest.Likes,
	}, hot.SizeTier, in.Targets)
	return hot, samples
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
