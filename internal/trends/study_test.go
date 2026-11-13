package trends

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func fixtureServer(t *testing.T, failPaths map[string]bool) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if failPaths[r.URL.Path] {
			http.Error(w, "synthetic failure", http.StatusInternalServerError)
			return
		}
		path := r.URL.Path
		if len(path) > len("/reddit-listing") && path[:len("/reddit-listing")] == "/reddit-listing" {
			path = "/reddit-listing"
		}
		name := map[string]string{
			"/google-news-vn":     "google_news_vn.xml",
			"/global-tech":        "global_tech.xml",
			"/youtube":            "youtube.xml",
			"/github":             "github_trending.html",
			"/reddit-token":       "reddit_token.json",
			"/reddit-listing":     "reddit_listing.json",
			"/hn/topstories.json": "hn_topstories.json",
			"/hn/item/101.json":   "hn_item_101.json",
			"/hn/item/102.json":   "hn_item_102.json",
		}[path]
		if name == "" {
			http.NotFound(w, r)
			return
		}
		body, err := os.ReadFile(filepath.Join("testdata", "study", name))
		if err != nil {
			t.Error(err)
			http.Error(w, "fixture", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(server.Close)
	return server
}

func TestFetchStudySourcesCoversEveryConfiguredSourceKind(t *testing.T) {
	server := fixtureServer(t, nil)
	opts := StudyOptions{
		HTTPClient:         server.Client(),
		Timeout:            5 * time.Second,
		MaxItemsPerSource:  5,
		Sources:            []string{"google_news_vn", "the_verge", "hacker_news", "github_trending", "youtube", "reddit_localllama", "google_news_topics", "vnexpress"},
		Queries:            []string{"AI"},
		YouTubeChannels:    []string{"UCsynthetic"},
		RedditClientID:     "id",
		RedditClientSecret: "secret",
		URLs: map[string]string{
			"google_news_vn":     server.URL + "/google-news-vn",
			"the_verge":          server.URL + "/global-tech",
			"hacker_news":        server.URL + "/hn",
			"github_trending":    server.URL + "/github",
			"youtube":            server.URL + "/youtube",
			"reddit_token":       server.URL + "/reddit-token",
			"reddit_api":         server.URL + "/reddit-listing",
			"google_news_topics": server.URL + "/global-tech",
			"vnexpress":          server.URL + "/google-news-vn",
		},
	}
	feed, err := FetchStudySources(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	sources := map[string]int{}
	for _, item := range feed.Items {
		sources[item.Source]++
	}
	for _, want := range []string{"google_news_vn", "the_verge", "hacker_news", "github_trending", "youtube", "reddit_localllama", "google_news_topics", "vnexpress"} {
		if sources[want] == 0 {
			t.Fatalf("source %s missing from %v (failed=%v skipped=%v)", want, sources, feed.Failed, feed.Skipped)
		}
	}
	if len(feed.Failed) != 0 || len(feed.Skipped) != 0 {
		t.Fatalf("failed=%v skipped=%v", feed.Failed, feed.Skipped)
	}
	seenTitle := false
	for _, item := range feed.Items {
		if item.Title == "Jensen Huang: GPU supply vẫn căng thẳng" {
			seenTitle = true
		}
	}
	if !seenTitle {
		t.Fatalf("expected fixture story missing: %+v", feed.Items)
	}
}

func TestFetchStudySourcesIsFailSoftPerSource(t *testing.T) {
	server := fixtureServer(t, map[string]bool{"/global-tech": true})
	feed, err := FetchStudySources(context.Background(), StudyOptions{
		HTTPClient:        server.Client(),
		Timeout:           5 * time.Second,
		MaxItemsPerSource: 5,
		Sources:           []string{"google_news_vn", "the_verge"},
		URLs: map[string]string{
			"google_news_vn": server.URL + "/google-news-vn",
			"the_verge":      server.URL + "/global-tech",
		},
	})
	if err != nil || len(feed.Items) == 0 || len(feed.Failed) != 1 || feed.Failed[0] != "the_verge" {
		t.Fatalf("items=%d failed=%v err=%v", len(feed.Items), feed.Failed, err)
	}
	// Every source failing is still an error, not a silent empty success.
	_, err = FetchStudySources(context.Background(), StudyOptions{
		HTTPClient: server.Client(), Timeout: time.Second,
		Sources: []string{"the_verge"},
		URLs:    map[string]string{"the_verge": server.URL + "/global-tech"},
	})
	if err == nil {
		t.Fatal("all-source failure accepted")
	}
}

func TestFetchStudySourcesSkipsRedditWithoutCredentials(t *testing.T) {
	feed, err := FetchStudySources(context.Background(), StudyOptions{
		Timeout: time.Second,
		Sources: []string{"reddit_openai"},
		URLs:    map[string]string{"reddit_token": "http://127.0.0.1:1/unused"},
	})
	if err != nil || len(feed.Skipped) != 1 || feed.Skipped[0] != "reddit_openai" {
		t.Fatalf("feed=%+v err=%v", feed, err)
	}
}
