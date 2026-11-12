package tools

import "testing"

func TestRankTrendsRespectsTopicsAndAvoidTopics(t *testing.T) {
	profile := TrendProfile{Include: []string{"AI", "LLM", "benchmark"}, Avoid: []string{"crypto", "airdrop"}}
	trends := []Trend{
		{Source: "hn", Title: "Local LLM benchmark results", URL: "https://x.invalid/1"},
		{Source: "gt", Title: "Crypto airdrop season", URL: "https://x.invalid/2"},
		{Source: "ph", Title: "Crypto project adds AI chatbot", URL: "https://x.invalid/3"},
		{Source: "vt", Title: "Cooking recipe of the day", URL: "https://x.invalid/4"},
		{Source: "hn", Title: "AI agent writes code", URL: "https://x.invalid/5"},
	}
	ranked := RankTrends(trends, profile)
	if len(ranked) != 3 {
		t.Fatalf("ranked=%d %+v", len(ranked), ranked)
	}
	for _, trend := range ranked {
		if trend.URL == "https://x.invalid/2" {
			t.Fatalf("avoid-only trend kept: %+v", ranked)
		}
	}
	if ranked[0].URL != "https://x.invalid/1" || ranked[1].URL != "https://x.invalid/5" || ranked[2].URL != "https://x.invalid/4" {
		t.Fatalf("order=%+v", ranked)
	}
	// Determinism: the same input yields the same order.
	again := RankTrends(trends, profile)
	for i := range ranked {
		if ranked[i].URL != again[i].URL {
			t.Fatalf("nondeterministic ranking: %+v vs %+v", ranked, again)
		}
	}
	// A phrase keyword matches only the phrase.
	if got := RankTrends(trends, TrendProfile{Include: []string{"local llm"}}); len(got) != 5 || got[0].URL != "https://x.invalid/1" {
		t.Fatalf("phrase rank=%+v", got)
	}
}

func TestRankStoriesClustersTheSameStoryAcrossSources(t *testing.T) {
	trends := []Trend{
		{Source: "google_news_vn", Title: "Jensen Huang says GPU supply is tight", URL: "https://news.invalid/1"},
		{Source: "the_verge", Title: "Nvidia CEO Jensen Huang on GPU supply", URL: "https://news.invalid/2"},
		{Source: "techcrunch", Title: "Jensen Huang: GPU supply remains tight", URL: "https://news.invalid/3"},
		{Source: "vnexpress", Title: "Giá vàng hôm nay", URL: "https://news.invalid/4"},
	}
	stories := RankStories(trends, TrendProfile{Include: []string{"GPU", "Nvidia", "Jensen Huang"}})
	if len(stories) != 2 {
		t.Fatalf("stories=%+v", stories)
	}
	if stories[0].Items != 3 || len(stories[0].Sources) != 3 || stories[0].Score < 6 {
		t.Fatalf("cluster=%+v", stories[0])
	}
	if stories[1].Items != 1 {
		t.Fatalf("single story=%+v", stories[1])
	}
}
