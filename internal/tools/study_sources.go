package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/canhta/threadwalker/internal/config"
)

// StudyOptions bounds one broad study fetch. Sources are independent: a
// failing source is reported and skipped, never turning a partial read into a
// total failure. Reddit needs an app-only OAuth client; unset credentials skip
// it without a network call.
type StudyOptions struct {
	HTTPClient         *http.Client
	Timeout            time.Duration
	MaxItemsPerSource  int
	Sources            []string
	Queries            []string
	YouTubeChannels    []string
	RedditClientID     string
	RedditClientSecret string
	// URLs overrides canonical endpoints for offline fixtures, keyed by source.
	URLs map[string]string
}

// StudyFeed is the fail-soft result of one broad fetch.
type StudyFeed struct {
	Items   []Trend  `json:"items"`
	Failed  []string `json:"failed_sources,omitempty"`
	Skipped []string `json:"skipped_sources,omitempty"`
}

const (
	studySourceTimeout  = 10 * time.Second
	studyOverallTimeout = 45 * time.Second
	studyMaxPerSource   = 8
	studyWorkers        = 4
	studyUserAgent      = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36"
)

var studyDefaultURLs = map[string]string{
	"google_news_vn":     "https://news.google.com/rss?hl=vi&gl=VN&ceid=VN:vi",
	"google_news_global": "https://news.google.com/rss?hl=en-US&gl=US&ceid=US:en",
	"vnexpress":          "https://vnexpress.net/rss/so-hoa.rss",
	"genk":               "https://genk.vn/rss/home.rss",
	"tinhte":             "https://tinhte.vn/rss/",
	"vietnamnet":         "https://vietnamnet.vn/rss/cong-nghe.rss",
	"the_verge":          "https://www.theverge.com/rss/index.xml",
	"techcrunch":         "https://techcrunch.com/feed/",
	"arstechnica":        "https://feeds.arstechnica.com/arstechnica/index",
	"hacker_news":        "https://hacker-news.firebaseio.com/v0",
	"product_hunt":       "https://www.producthunt.com/feed",
	"google_trends":      "https://trends.google.com/trending/rss?geo=VN",
	"github_trending":    "https://github.com/trending?since=daily",
	"reddit_token":       "https://www.reddit.com/api/v1/access_token",
	"reddit_api":         "https://oauth.reddit.com",
}

// StudySourcesFromTemplate resolves per-agent sources, queries and credentials.
func StudySourcesFromTemplate(tmpl config.Template) StudyOptions {
	sources := tmpl.Study.Sources
	if len(sources) == 0 {
		sources = append([]string{}, config.DefaultStudySources...)
	}
	include, _ := tmpl.TrendKeywords()
	queries := include
	if len(queries) > 3 {
		queries = queries[:3]
	}
	channels := tmpl.Study.YouTubeChannels
	if len(channels) == 0 {
		channels = append([]string{}, config.DefaultYouTubeChannels...)
	}
	return StudyOptions{
		Timeout:            studySourceTimeout,
		MaxItemsPerSource:  studyMaxPerSource,
		Sources:            sources,
		Queries:            queries,
		YouTubeChannels:    channels,
		RedditClientID:     tmpl.Study.RedditClientID.Expose(),
		RedditClientSecret: tmpl.Study.RedditClientSecret.Expose(),
	}
}

type studySourceResult struct {
	source string
	items  []Trend
	failed bool
	skip   bool
}

// FetchStudySources fetches every enabled source with a per-source deadline and
// a shared overall deadline. Results keep canonical source order.
func FetchStudySources(ctx context.Context, opts StudyOptions) (StudyFeed, error) {
	if ctx == nil {
		return StudyFeed{}, errors.New("fetch study sources: nil context")
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = studySourceTimeout
	}
	if opts.MaxItemsPerSource <= 0 || opts.MaxItemsPerSource > 50 {
		opts.MaxItemsPerSource = studyMaxPerSource
	}
	ctx, cancel := context.WithTimeout(ctx, studyOverallTimeout)
	defer cancel()
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	endpoint := func(id string) string {
		if raw := strings.TrimSpace(opts.URLs[id]); raw != "" {
			return raw
		}
		if id == "google_news_topics" {
			return ""
		}
		return studyDefaultURLs[id]
	}
	sources := append([]string{}, opts.Sources...)
	if len(sources) == 0 {
		sources = append([]string{}, config.DefaultStudySources...)
	}
	results := make([]studySourceResult, len(sources))
	sem := make(chan struct{}, studyWorkers)
	var wg sync.WaitGroup
	for index, source := range sources {
		results[index].source = source
		wg.Add(1)
		go func(index int, source string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			items, skip, err := fetchStudySource(ctx, client, opts, source, endpoint(source), timeout)
			switch {
			case skip:
				results[index].skip = true
			case err != nil:
				results[index].failed = true
			default:
				results[index].items = items
			}
		}(index, source)
	}
	wg.Wait()
	feed := StudyFeed{Items: []Trend{}, Failed: []string{}, Skipped: []string{}}
	for _, result := range results {
		switch {
		case result.skip:
			feed.Skipped = append(feed.Skipped, result.source)
		case result.failed:
			feed.Failed = append(feed.Failed, result.source)
		default:
			feed.Items = append(feed.Items, result.items...)
		}
	}
	if len(feed.Items) == 0 && len(feed.Failed) == len(sources) {
		return feed, errors.New("fetch study sources: every source failed")
	}
	return feed, nil
}

func fetchStudySource(ctx context.Context, client *http.Client, opts StudyOptions, source, endpoint string, timeout time.Duration) ([]Trend, bool, error) {
	sourceCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	switch source {
	case "hacker_news":
		items, err := fetchHackerNews(sourceCtx, client, endpoint, opts.MaxItemsPerSource)
		return items, false, err
	case "reddit_localllama", "reddit_openai", "reddit_singularity":
		if opts.RedditClientID == "" || opts.RedditClientSecret == "" {
			return nil, true, nil
		}
		return fetchReddit(sourceCtx, client, opts, source)
	case "github_trending":
		items, err := fetchGitHubTrending(sourceCtx, client, endpoint, opts.MaxItemsPerSource)
		return items, false, err
	case "youtube":
		var all []Trend
		for _, channel := range opts.YouTubeChannels {
			if len(all) >= opts.MaxItemsPerSource {
				break
			}
			feedURL := "https://www.youtube.com/feeds/videos.xml?channel_id=" + url.QueryEscape(channel)
			if custom := opts.URLs["youtube"]; custom != "" {
				feedURL = custom
			}
			items, err := fetchStudyRSS(sourceCtx, client, feedURL, "youtube", opts.MaxItemsPerSource-len(all))
			if err != nil {
				if len(all) == 0 {
					return nil, false, err
				}
				break
			}
			all = append(all, items...)
			if opts.URLs["youtube"] != "" {
				break
			}
		}
		return all, false, nil
	case "google_news_topics":
		var all []Trend
		if len(opts.Queries) == 0 {
			opts.Queries = []string{"AI"}
		}
		global := opts.URLs["google_news_global"] != ""
		for _, query := range opts.Queries {
			if len(all) >= opts.MaxItemsPerSource {
				break
			}
			raw := opts.URLs["google_news_topics"]
			if raw == "" {
				raw = "https://news.google.com/rss/search?q=" + url.QueryEscape(query) + "&hl=vi&gl=VN&ceid=VN:vi"
				if global {
					raw = "https://news.google.com/rss/search?q=" + url.QueryEscape(query) + "&hl=en-US&gl=US&ceid=US:en"
				}
			}
			items, err := fetchStudyRSS(sourceCtx, client, raw, source, opts.MaxItemsPerSource-len(all))
			if err != nil {
				if len(all) == 0 {
					return nil, false, err
				}
				break
			}
			all = append(all, items...)
			if opts.URLs["google_news_topics"] != "" {
				break
			}
		}
		return all, false, nil
	default:
		if endpoint == "" {
			return nil, false, errors.New("fetch study source: no endpoint")
		}
		items, err := fetchStudyRSS(sourceCtx, client, endpoint, source, opts.MaxItemsPerSource)
		return items, false, err
	}
}

func fetchStudyRSS(ctx context.Context, client *http.Client, endpoint, source string, limit int) ([]Trend, error) {
	body, err := fetchStudyBody(ctx, client, endpoint)
	if err != nil {
		return nil, err
	}
	records, err := parseTrendFeed(body)
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", source, err)
	}
	trends := make([]Trend, 0, limit)
	for _, record := range records {
		title := strings.TrimSpace(html.UnescapeString(record.title))
		link := strings.TrimSpace(record.link)
		if title == "" || link == "" {
			continue
		}
		trends = append(trends, Trend{Source: source, Title: title, URL: link})
		if len(trends) == limit {
			break
		}
	}
	return trends, nil
}

func fetchStudyBody(ctx context.Context, client *http.Client, endpoint string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, errors.New("build request")
	}
	request.Header.Set("User-Agent", studyUserAgent)
	request.Header.Set("Accept", "application/json, application/atom+xml, application/rss+xml, application/xml, text/xml, text/html;q=0.8, */*;q=0.1")
	response, err := client.Do(request)
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return nil, contextErr
		}
		return nil, errors.New("request failed")
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("unexpected HTTP status %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxTrendResponseBytes+1))
	if err != nil {
		return nil, errors.New("read response body")
	}
	if len(body) > maxTrendResponseBytes {
		return nil, errors.New("response body too large")
	}
	return body, nil
}

var githubArticle = regexp.MustCompile(`(?s)<article[^>]*>(.*?)</article>`)
var githubRepo = regexp.MustCompile(`href="/([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+)"`)
var githubDescription = regexp.MustCompile(`(?s)<p[^>]*class="[^"]*col-9[^"]*"[^>]*>(.*?)</p>`)
var htmlTags = regexp.MustCompile(`<[^>]*>`)

// fetchGitHubTrending parses the public trending page. Only repository links,
// names and descriptions are read; no selector or script is guessed beyond the
// measured page shape, and malformed blocks are skipped.
func fetchGitHubTrending(ctx context.Context, client *http.Client, endpoint string, limit int) ([]Trend, error) {
	body, err := fetchStudyBody(ctx, client, endpoint)
	if err != nil {
		return nil, err
	}
	blocks := githubArticle.FindAllStringSubmatch(string(body), -1)
	trends := make([]Trend, 0, limit)
	seen := map[string]bool{}
	for _, block := range blocks {
		match := githubRepo.FindStringSubmatch(block[1])
		if match == nil {
			continue
		}
		owner, repo := match[1], match[2]
		if owner == "login" || owner == "sponsors" || seen[owner+"/"+repo] {
			continue
		}
		seen[owner+"/"+repo] = true
		description := ""
		if desc := githubDescription.FindStringSubmatch(block[1]); desc != nil {
			description = strings.TrimSpace(html.UnescapeString(htmlTags.ReplaceAllString(desc[1], " ")))
		}
		title := owner + "/" + repo
		if description != "" {
			title += ": " + description
		}
		trends = append(trends, Trend{Source: "github_trending", Title: title, URL: "https://github.com/" + owner + "/" + repo})
		if len(trends) == limit {
			break
		}
	}
	if len(trends) == 0 {
		return nil, errors.New("decode github trending: no repositories")
	}
	return trends, nil
}

// redditSubs maps the canonical source to a subreddit name.
var redditSubs = map[string]string{
	"reddit_localllama":  "LocalLLaMA",
	"reddit_openai":      "OpenAI",
	"reddit_singularity": "singularity",
}

func fetchReddit(ctx context.Context, client *http.Client, opts StudyOptions, source string) ([]Trend, bool, error) {
	tokenURL := opts.URLs["reddit_token"]
	if tokenURL == "" {
		tokenURL = studyDefaultURLs["reddit_token"]
	}
	form := url.Values{"grant_type": {"client_credentials"}}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, false, errors.New("reddit: build token request")
	}
	request.SetBasicAuth(opts.RedditClientID, opts.RedditClientSecret)
	request.Header.Set("User-Agent", studyUserAgent)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := client.Do(request)
	if err != nil {
		return nil, false, errors.New("reddit: token request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("reddit: token status %d", response.StatusCode)
	}
	var token struct {
		AccessToken string `json:"access_token"`
	}
	if err = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&token); err != nil || token.AccessToken == "" {
		return nil, false, errors.New("reddit: invalid token response")
	}
	base := opts.URLs["reddit_api"]
	if base == "" {
		base = studyDefaultURLs["reddit_api"]
	}
	endpoint := strings.TrimRight(base, "/") + "/r/" + redditSubs[source] + "/hot?limit=" + fmt.Sprint(opts.MaxItemsPerSource)
	request, err = http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, false, errors.New("reddit: build request")
	}
	request.Header.Set("Authorization", "Bearer "+token.AccessToken)
	request.Header.Set("User-Agent", studyUserAgent)
	response, err = client.Do(request)
	if err != nil {
		return nil, false, errors.New("reddit: request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("reddit: status %d", response.StatusCode)
	}
	var listing struct {
		Data struct {
			Children []struct {
				Data struct {
					Title     string `json:"title"`
					URL       string `json:"url"`
					Permalink string `json:"permalink"`
					Subreddit string `json:"subreddit"`
				} `json:"data"`
			} `json:"children"`
		} `json:"data"`
	}
	if err = json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&listing); err != nil {
		return nil, false, errors.New("reddit: invalid listing")
	}
	trends := make([]Trend, 0, opts.MaxItemsPerSource)
	for _, child := range listing.Data.Children {
		title := strings.TrimSpace(child.Data.Title)
		link := child.Data.URL
		if link == "" && child.Data.Permalink != "" {
			link = "https://www.reddit.com" + child.Data.Permalink
		}
		if title == "" || link == "" {
			continue
		}
		trends = append(trends, Trend{Source: source, Title: title, URL: link})
		if len(trends) == opts.MaxItemsPerSource {
			break
		}
	}
	return trends, false, nil
}

// sortStrings keeps test/diagnostic output stable without changing source order.
func sortStrings(values []string) []string {
	out := append([]string{}, values...)
	sort.Strings(out)
	return out
}
