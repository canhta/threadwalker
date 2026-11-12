package tools

import (
	"sort"
	"strings"
	"unicode"

	"github.com/canhta/threadwalker/internal/config"
)

// TrendProfile is the harness-owned ranking input built from template fields.
// It never changes between items of one fetch, so ranking is deterministic.
type TrendProfile struct {
	Include []string
	Avoid   []string
}

// RankTrends ranks trends for the agent's topics and removes avoid-topic hits.
// Matching is case-insensitive token/phrase matching with no model call and no
// network access. A trend's source-native score and URL never affect ranking.
func RankTrends(trends []Trend, profile TrendProfile) []Trend {
	if len(trends) == 0 {
		return []Trend{}
	}
	include := cleanKeywords(profile.Include)
	avoid := cleanKeywords(profile.Avoid)
	scored := make([]struct {
		trend Trend
		score int
	}, 0, len(trends))
	for _, trend := range trends {
		title := normalizeTrendText(trend.Title)
		tokens := trendTokens(title)
		score := 0
		for _, keyword := range include {
			score += trendKeywordScore(title, tokens, keyword)
		}
		for _, keyword := range avoid {
			score -= trendKeywordScore(title, tokens, keyword) * 2
		}
		if score < 0 {
			continue
		}
		scored = append(scored, struct {
			trend Trend
			score int
		}{trend, score})
	}
	sort.SliceStable(scored, func(i, j int) bool { return scored[i].score > scored[j].score })
	out := make([]Trend, 0, len(scored))
	for _, item := range scored {
		out = append(out, item.trend)
	}
	return out
}

// Story is one clustered trend: the same event reported by several sources
// becomes one item with a higher score, so the agent sees hot stories rather
// than four copies.
type Story struct {
	Title   string   `json:"title"`
	URL     string   `json:"url"`
	Sources []string `json:"sources"`
	Score   int      `json:"score"`
	Items   int      `json:"items"`
}

// RankStories scores, filters and clusters trend items deterministically.
func RankStories(trends []Trend, profile TrendProfile) []Story {
	include := cleanKeywords(profile.Include)
	avoid := cleanKeywords(profile.Avoid)
	// Generic topic words must not merge unrelated stories; clustering keeps
	// distinctive tokens such as names, products and events.
	generic := map[string]bool{}
	for _, keyword := range include {
		if !strings.Contains(keyword, " ") {
			generic[keyword] = true
		}
	}
	type scored struct {
		item  Trend
		score int
		toks  map[string]bool
	}
	items := make([]scored, 0, len(trends))
	for _, trend := range trends {
		title := normalizeTrendText(trend.Title)
		tokens := trendTokens(title)
		score := 0
		for _, keyword := range include {
			score += trendKeywordScore(title, tokens, keyword)
		}
		for _, keyword := range avoid {
			score -= trendKeywordScore(title, tokens, keyword) * 2
		}
		if score < 0 {
			continue
		}
		items = append(items, scored{trend, score, storyTokens(trend.Title, generic)})
	}
	type cluster struct {
		story Story
		seen  map[string]bool
		best  int
		index int
	}
	var clusters []*cluster
	for index, item := range items {
		var target *cluster
		for _, candidate := range clusters {
			if shareStoryTokens(candidate.seen, item.toks) {
				target = candidate
				break
			}
		}
		if target == nil {
			clusters = append(clusters, &cluster{
				story: Story{Title: item.item.Title, URL: item.item.URL, Sources: []string{item.item.Source}, Score: item.score, Items: 1},
				seen:  item.toks,
				best:  item.score,
				index: index,
			})
			continue
		}
		target.story.Items++
		if !hasString(target.story.Sources, item.item.Source) {
			target.story.Sources = append(target.story.Sources, item.item.Source)
		}
		if item.score > target.best {
			target.best = item.score
			target.story.Title = item.item.Title
			target.story.URL = item.item.URL
		}
	}
	for _, c := range clusters {
		c.story.Score = c.best + 2*(len(c.story.Sources)-1) + minInt(c.story.Items-1, 3)
	}
	sort.SliceStable(clusters, func(i, j int) bool {
		if clusters[i].story.Score != clusters[j].story.Score {
			return clusters[i].story.Score > clusters[j].story.Score
		}
		return clusters[i].index < clusters[j].index
	})
	out := make([]Story, 0, len(clusters))
	for _, c := range clusters {
		out = append(out, c.story)
	}
	return out
}

func hasString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

var storyStopwords = func() map[string]bool {
	set := map[string]bool{}
	for _, word := range strings.Fields("the a an and or of to in on for with is are be by that this it as at from new says say said after before over under about into its their his her how why what when who will would can could may might more most than then them they you your our we us next top best vs") {
		set[word] = true
	}
	return set
}()

func storyTokens(title string, generic map[string]bool) map[string]bool {
	set := map[string]bool{}
	for _, token := range config.TopicTokens(title) {
		if len([]rune(token)) < 3 || storyStopwords[token] || generic[token] {
			continue
		}
		set[token] = true
	}
	return set
}

func shareStoryTokens(a, b map[string]bool) bool {
	shared := 0
	for token := range a {
		if b[token] {
			shared++
		}
	}
	return shared >= 2 || shared == 1 && minInt(len(a), len(b)) == 1
}

func cleanKeywords(keywords []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, keyword := range keywords {
		normalized := normalizeTrendText(keyword)
		if normalized == "" || seen[normalized] {
			continue
		}
		seen[normalized] = true
		out = append(out, normalized)
	}
	return out
}

func normalizeTrendText(text string) string {
	return strings.Join(strings.Fields(strings.ToLower(text)), " ")
}

func trendTokens(normalized string) []string {
	return strings.FieldsFunc(normalized, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

// trendKeywordScore gives a phrase or exact token full weight and a token
// prefix half weight, so "dev" also tracks "developer" without matching
// unrelated words. Each keyword counts at most once per title.
func trendKeywordScore(title string, tokens []string, keyword string) int {
	if strings.Contains(keyword, " ") {
		if strings.Contains(title, keyword) {
			return 3
		}
		return 0
	}
	for _, token := range tokens {
		if token == keyword {
			return 3
		}
	}
	for _, token := range tokens {
		if len([]rune(keyword)) >= 3 && strings.HasPrefix(token, keyword) {
			return 1
		}
	}
	return 0
}
